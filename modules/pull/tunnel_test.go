package pull

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/proxyegress"
)

func TestHTTPSProxyTLSHandshakeTimesOut(t *testing.T) {
	client, silentProxy := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = silentProxy.Close() }()

	started := time.Now()
	_, err := handshakeProxyTLS(context.Background(), client, "proxy.example", 25*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("TLS handshake error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("TLS handshake took %s", elapsed)
	}
}

func TestDialPinnedHTTPSProxyHandshakeFailureReturnsError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	served := make(chan struct{})
	go func() {
		defer close(served)
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_, _ = connection.Write([]byte("not TLS"))
		_ = connection.Close()
	}()

	connection, err := dialPinnedTarget(context.Background(), &url.URL{
		Scheme: "https",
		Host:   listener.Addr().String(),
	}, "192.0.2.1:443")
	if connection != nil {
		_ = connection.Close()
	}
	if err == nil {
		t.Fatal("HTTPS proxy handshake succeeded with invalid TLS")
	}
	<-served
}

func TestDialPinnedSOCKSNegotiationTimesOut(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, connection)
	}()

	proxyURL := &url.URL{Scheme: "socks5", Host: listener.Addr().String()}
	type result struct {
		connection net.Conn
		err        error
	}
	completed := make(chan result, 1)
	go func() {
		connection, err := dialPinnedThroughSOCKS(
			context.Background(), proxyURL, "192.0.2.1:443", 25*time.Millisecond,
		)
		completed <- result{connection: connection, err: err}
	}()

	select {
	case got := <-completed:
		if got.connection != nil {
			_ = got.connection.Close()
		}
		if got.err == nil {
			t.Fatal("silent SOCKS proxy unexpectedly completed negotiation")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("silent SOCKS proxy did not honor the dial timeout")
	}
}

func TestDialPinnedChainsThroughHTTPProxy(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	go func() {
		connection, err := target.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.Copy(connection, connection)
	}()

	connectTarget := make(chan string, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-pass"))
		if request.Header.Get("Proxy-Authorization") != expectedAuth {
			http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
			return
		}
		connectTarget <- request.Host
		upstream, err := net.Dial("tcp", request.Host)
		if err != nil {
			http.Error(w, "dial failed", http.StatusBadGateway)
			return
		}
		downstream, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			_ = upstream.Close()
			return
		}
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		go func() {
			defer func() { _ = downstream.Close() }()
			defer func() { _ = upstream.Close() }()
			go func() { _, _ = io.Copy(upstream, downstream) }()
			_, _ = io.Copy(downstream, upstream)
		}()
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("proxy-user", "proxy-pass")
	router, err := proxyegress.NewRouter(proxyegress.Config{
		Default: "guard",
		Profiles: []proxyegress.Profile{{
			ID: "guard", URL: proxyURL.String(),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Options{
		Allowed: map[string]struct{}{"127.0.0.1": {}},
		Router:  router,
	})
	connection, err := client.DialPinned(context.Background(), "https://"+target.Addr().String(), "channel")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "ping" {
		t.Fatalf("tunnel response = %q, want ping", response)
	}
	if got := <-connectTarget; got != target.Addr().String() || !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("proxy CONNECT target = %q, want pinned IP %q", got, target.Addr())
	}
}

func TestDialPinnedChainsThroughSOCKSProxy(t *testing.T) {
	target, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	go func() {
		connection, err := target.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = io.Copy(connection, connection)
	}()
	_, targetPort, err := net.SplitHostPort(target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	connectTarget := make(chan string, 1)
	socksAddr := startTestSOCKS5(t, connectTarget)
	router, err := proxyegress.NewRouter(proxyegress.Config{
		Default: "socks",
		Profiles: []proxyegress.Profile{{
			ID: "socks", URL: "socks5h://" + socksAddr,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Options{
		Allowed: map[string]struct{}{"localhost": {}},
		Router:  router,
	})
	connection, err := client.DialPinned(context.Background(), "https://localhost:"+targetPort, "channel")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(connection, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "ping" {
		t.Fatalf("tunnel response = %q, want ping", response)
	}
	got := <-connectTarget
	host, port, err := net.SplitHostPort(got)
	if err != nil || net.ParseIP(host) == nil || port != targetPort {
		t.Fatalf("SOCKS5H target = %q, want locally pinned IP on port %s", got, targetPort)
	}
}

func TestDialPinnedRejectsUnsupportedProxyScheme(t *testing.T) {
	_, err := dialPinnedTarget(
		context.Background(),
		&url.URL{Scheme: "quic", Host: "127.0.0.1:1080"},
		"127.0.0.1:443",
	)
	if err == nil || !strings.Contains(err.Error(), "CONNECT tunnel cannot use quic proxy") {
		t.Fatalf("unsupported scheme error = %v", err)
	}
}

func startTestSOCKS5(t *testing.T, connectTarget chan<- string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = client.Close() }()
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(client, greeting); err != nil {
			return
		}
		if _, err := io.ReadFull(client, make([]byte, greeting[1])); err != nil {
			return
		}
		if _, err := client.Write([]byte{0x05, 0x00}); err != nil {
			return
		}
		request := make([]byte, 4)
		if _, err := io.ReadFull(client, request); err != nil {
			return
		}
		if request[1] != 0x01 {
			return
		}
		var host string
		switch request[3] {
		case 0x01:
			destination := make([]byte, 4)
			if _, err := io.ReadFull(client, destination); err != nil {
				return
			}
			host = net.IP(destination).String()
		case 0x03:
			var size [1]byte
			if _, err := io.ReadFull(client, size[:]); err != nil {
				return
			}
			destination := make([]byte, int(size[0]))
			if _, err := io.ReadFull(client, destination); err != nil {
				return
			}
			host = string(destination)
		case 0x04:
			destination := make([]byte, net.IPv6len)
			if _, err := io.ReadFull(client, destination); err != nil {
				return
			}
			host = net.IP(destination).String()
		default:
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(client, port); err != nil {
			return
		}
		address := net.JoinHostPort(
			host,
			strconv.Itoa(int(port[0])<<8|int(port[1])),
		)
		connectTarget <- address
		upstream, err := net.Dial("tcp", address)
		if err != nil {
			_, _ = client.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			return
		}
		defer func() { _ = upstream.Close() }()
		if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
			return
		}
		go func() { _, _ = io.Copy(upstream, client) }()
		_, _ = io.Copy(client, upstream)
	}()
	return listener.Addr().String()
}
