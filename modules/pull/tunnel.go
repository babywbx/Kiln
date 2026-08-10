package pull

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/security"
	"golang.org/x/net/proxy"
)

func (c *Client) DialPinned(ctx context.Context, rawURL, channelID string) (net.Conn, error) {
	target, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(target.Scheme, "https") {
		return nil, fmt.Errorf("CONNECT target must use https")
	}
	pinned, err := security.PinPublicProbeURL(ctx, rawURL, c.allowed)
	if err != nil {
		return nil, err
	}
	port := target.Port()
	if port == "" {
		port = "443"
	}
	address := net.JoinHostPort(pinned.Hostname(), port)
	if c.router == nil {
		return dialPinnedTarget(ctx, nil, address)
	}
	decision := c.router.Resolve(rawURL, channelID)
	return dialPinnedTarget(ctx, decision.ProxyURL, address)
}

func dialPinnedTarget(ctx context.Context, proxyURL *url.URL, address string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || net.ParseIP(host) == nil {
		return nil, fmt.Errorf("pinned target requires an IP address")
	}
	if proxyURL == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	}
	scheme := strings.ToLower(proxyURL.Scheme)
	switch scheme {
	case "http", "https":
	case "socks5", "socks5h":
		return dialPinnedThroughSOCKS(ctx, proxyURL, address)
	default:
		return nil, fmt.Errorf("CONNECT tunnel cannot use %s proxy", scheme)
	}
	proxyAddress := proxyURL.Host
	if proxyURL.Port() == "" {
		port := "80"
		if scheme == "https" {
			port = "443"
		}
		proxyAddress = net.JoinHostPort(proxyURL.Hostname(), port)
	}
	connection, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", proxyAddress)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = connection.Close()
		}
	}()
	if scheme == "https" {
		tlsConnection := tls.Client(connection, &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: proxyURL.Hostname(),
		})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return nil, err
		}
		connection = tlsConnection
	}
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	request := "CONNECT " + address + " HTTP/1.1\r\nHost: " + address + "\r\n"
	if proxyURL.User != nil {
		password, _ := proxyURL.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(proxyURL.User.Username() + ":" + password))
		request += "Proxy-Authorization: Basic " + credentials + "\r\n"
	}
	if _, err := io.WriteString(connection, request+"\r\n"); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		return nil, fmt.Errorf("proxy CONNECT failed with %s", response.Status)
	}
	_ = connection.SetDeadline(time.Time{})
	failed = false
	return &bufferedConn{Conn: connection, reader: reader}, nil
}

// address is already a pinned IP, so socks5 and socks5h behave identically.
func dialPinnedThroughSOCKS(ctx context.Context, proxyURL *url.URL, address string) (net.Conn, error) {
	u := *proxyURL
	u.Scheme = "socks5"
	dialer, err := proxy.FromURL(&u, &net.Dialer{Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, "tcp", address)
	}
	return dialer.Dial("tcp", address)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(buffer []byte) (int, error) {
	return c.reader.Read(buffer)
}
