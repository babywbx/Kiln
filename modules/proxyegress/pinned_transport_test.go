package proxyegress

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestPinnedTransportUsesSeparateTLSNamesForHTTPSProxyAndTarget(t *testing.T) {
	proxyCertificate, proxyLeaf := testCertificate(t, "127.0.0.1")
	targetCertificate, targetLeaf := testCertificate(t, "localhost")

	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy method = %s, want CONNECT", r.Method)
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		connectHost, connectPort, err := net.SplitHostPort(r.Host)
		if err != nil || net.ParseIP(connectHost) == nil || connectPort != "443" {
			t.Errorf("CONNECT target = %q, want pinned IP on port 443", r.Host)
			return
		}
		connection, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack proxy connection: %v", err)
			return
		}
		defer func() { _ = connection.Close() }()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buffered.Flush(); err != nil {
			t.Errorf("flush CONNECT response: %v", err)
			return
		}

		target := tls.Server(connection, &tls.Config{
			Certificates: []tls.Certificate{targetCertificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := target.HandshakeContext(context.Background()); err != nil {
			t.Errorf("target TLS handshake: %v", err)
			return
		}
		defer func() { _ = target.Close() }()
		request, err := http.ReadRequest(bufio.NewReader(target))
		if err != nil {
			t.Errorf("read tunneled request: %v", err)
			return
		}
		_ = request.Body.Close()
		_, _ = io.WriteString(target, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
	}))
	proxy.TLS = &tls.Config{
		Certificates: []tls.Certificate{proxyCertificate},
		MinVersion:   tls.VersionTLS12,
	}
	proxy.StartTLS()
	defer proxy.Close()

	roots := x509.NewCertPool()
	roots.AddCert(proxyLeaf)
	roots.AddCert(targetLeaf)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: NewPinnedTransport(&http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		}, nil, "", map[string]struct{}{"localhost": {}}),
	}
	response, err := client.Get("https://localhost/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
}

func TestPinnedTransportDialsTheValidatedAddress(t *testing.T) {
	receivedHost := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost <- r.Host
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	pinnedAddress := net.JoinHostPort("192.0.2.10", originURL.Port())
	transport := newPinnedTransport(&http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != pinnedAddress {
				return nil, fmt.Errorf("dialed %s instead of pinned address", address)
			}
			return (&net.Dialer{}).DialContext(ctx, network, origin.Listener.Addr().String())
		},
	}, nil, "", nil, func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
		resolved, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		resolved.Host = pinnedAddress
		return resolved, nil
	})
	client := &http.Client{Transport: transport}
	response, err := client.Get("http://rebind.invalid:" + originURL.Port() + "/asset")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if got, want := <-receivedHost, "rebind.invalid:"+originURL.Port(); got != want {
		t.Fatalf("Host = %q, want %q", got, want)
	}
}

func TestPinnedTransportReusesConnectionAfterRepinning(t *testing.T) {
	certificate, leaf := testCertificate(t, "origin.example")
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	origin.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	origin.StartTLS()
	defer origin.Close()
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	pins := 0
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	transport := newPinnedTransport(&http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}, nil, "", nil,
		func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
			pins++
			pinned, err := url.Parse(rawURL)
			if err != nil {
				return nil, err
			}
			pinned.Host = originURL.Host
			return pinned, nil
		})
	client := &http.Client{Transport: transport}
	for i := 0; i < 2; i++ {
		reused := false
		request, err := http.NewRequest(http.MethodGet, "https://origin.example:"+originURL.Port()+"/asset", nil)
		if err != nil {
			t.Fatal(err)
		}
		request = request.WithContext(httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
			GotConn: func(info httptrace.GotConnInfo) { reused = info.Reused },
		}))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if i == 1 && !reused {
			t.Fatal("second request opened a new connection")
		}
	}
	if pins != 2 {
		t.Fatalf("destination pins = %d, want 2", pins)
	}
}

func TestPinnedTransportKeepsTLSHandshakeTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	release := make(chan struct{})
	defer close(release)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = connection.Close() }()
		<-release
	}()

	transport := newPinnedTransport(&http.Transport{TLSHandshakeTimeout: 100 * time.Millisecond}, nil, "", nil,
		func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
			pinned, err := url.Parse(rawURL)
			if err != nil {
				return nil, err
			}
			pinned.Host = listener.Addr().String()
			return pinned, nil
		})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://origin.example/", nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	response, err := (&http.Client{Transport: transport}).Do(request)
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("silent TLS peer completed a request")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("TLS handshake timeout took %v", elapsed)
	}
}

func TestPinnedTransportReplacesStaleCachedTransports(t *testing.T) {
	base := &http.Transport{}
	transport := newPinnedTransport(base, nil, "", nil,
		func(context.Context, string, map[string]struct{}) (*url.URL, error) { return nil, nil }).(*pinnedTransport)
	key := pinnedTransportKey{base: base, scheme: "https", authority: "origin.example", serverName: "origin.example"}
	first := transport.transport(key, "192.0.2.1", 0)
	if reused := transport.transport(key, "192.0.2.1", 0); reused != first {
		t.Fatal("unchanged pin did not reuse its transport")
	}
	if replaced := transport.transport(key, "198.51.100.1", 0); replaced == first {
		t.Fatal("changed pin reused its old transport")
	}
	if got := len(transport.transports); got != 1 {
		t.Fatalf("cached transports after pin change = %d, want 1", got)
	}

	other := key
	other.authority = "other.example"
	other.serverName = "other.example"
	transport.transport(other, "203.0.113.1", 0)
	transport.transport(key, "198.51.100.1", 1)
	if got := len(transport.transports); got != 1 {
		t.Fatalf("cached transports after reload = %d, want 1", got)
	}
	if stale := transport.transport(other, "203.0.113.1", 0); stale != nil {
		t.Fatal("stale router generation recreated a transport")
	}
}

func TestPinnedTransportRejectsRebindingBeforeReuse(t *testing.T) {
	hits := 0
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()
	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	pins := 0
	transport := newPinnedTransport(&http.Transport{}, nil, "", nil,
		func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
			pins++
			if pins == 2 {
				return nil, fmt.Errorf("resolved to a non-public address")
			}
			pinned, err := url.Parse(rawURL)
			if err != nil {
				return nil, err
			}
			pinned.Host = originURL.Host
			return pinned, nil
		})
	client := &http.Client{Transport: transport}
	response, err := client.Get("http://origin.example:" + originURL.Port() + "/asset")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	response, err = client.Get("http://origin.example:" + originURL.Port() + "/asset")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("rebound destination reused the prior connection")
	}
	if hits != 1 {
		t.Fatalf("origin hits = %d, want 1", hits)
	}
}

func testCertificate(t *testing.T, host string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if ip := net.ParseIP(host); ip != nil {
		template.IPAddresses = []net.IP{ip}
	} else {
		template.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, leaf
}

func TestPinnedTransportPinsConnectTargetThroughHTTPProxy(t *testing.T) {
	connectTarget := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		connectTarget <- request.Host
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := newPinnedTransport(&http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}, nil, "", nil, func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
		resolved, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		resolved.Host = "198.18.5.255:443"
		return resolved, nil
	})
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	response, err := client.Get("https://cdn.example.com/manifest.mpd")
	if response != nil {
		_ = response.Body.Close()
	}
	_ = err

	select {
	case got := <-connectTarget:
		if got != "198.18.5.255:443" {
			t.Fatalf("CONNECT target = %q, want pinned address 198.18.5.255:443", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("proxy never received a CONNECT request")
	}
}

func TestPinnedTransportRejectsPlainHTTPThroughHTTPProxy(t *testing.T) {
	reached := make(chan struct{}, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached <- struct{}{}
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := newPinnedTransport(&http.Transport{Proxy: http.ProxyURL(proxyURL)}, nil, "", nil,
		func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
			resolved, err := url.Parse(rawURL)
			if err != nil {
				return nil, err
			}
			resolved.Host = "198.18.5.255:8080"
			return resolved, nil
		})
	response, err := (&http.Client{Timeout: 5 * time.Second, Transport: transport}).Get(
		"http://cdn.example.com:8080/manifest.mpd",
	)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, ErrUnpinnableProxyTarget) {
		t.Fatalf("plain HTTP proxy error = %v, want fail-closed error", err)
	}
	select {
	case <-reached:
		t.Fatal("proxy was reached for an unpinnable plain HTTP target")
	default:
	}
}

func TestPinnedTransportPoolIsBounded(t *testing.T) {
	base := &http.Transport{}
	transport := &pinnedTransport{transports: map[pinnedTransportKey]pinnedTransportEntry{}}
	for i := range maxPinnedTransports + 1 {
		key := pinnedTransportKey{base: base, scheme: "http", authority: fmt.Sprintf("cdn-%d.example", i)}
		if transport.transport(key, "198.18.5.255", 0) == nil {
			t.Fatalf("transport %d was not created", i)
		}
	}
	if got := len(transport.transports); got > maxPinnedTransports {
		t.Fatalf("transport pool size = %d, want at most %d", got, maxPinnedTransports)
	}
}

func TestPinnedTransportPreservesLegacyTLSDialer(t *testing.T) {
	base := &http.Transport{}
	base.DialTLS = func(string, string) (net.Conn, error) { //nolint:staticcheck // Exercise the legacy hook.
		return nil, errors.New("legacy dialer")
	}
	transport := &pinnedTransport{transports: map[pinnedTransportKey]pinnedTransportEntry{}}
	key := pinnedTransportKey{
		base: base, scheme: "https", authority: "cdn.example:443", serverName: "cdn.example",
		proxy: "https://proxy.example:443", proxyViaTransport: true,
	}
	got := transport.transport(key, "198.18.5.255", 0)
	if got == nil || got.DialTLSContext != nil || got.DialTLS == nil { //nolint:staticcheck // Verify the legacy hook.
		t.Fatalf("legacy TLS dialer was replaced: %#v", got)
	}
}

func TestPinnedTransportPinsTargetThroughSOCKSProxy(t *testing.T) {
	tests := []struct {
		scheme string
		want   string
	}{
		{scheme: "socks5", want: "198.18.5.255:8080"},
		{scheme: "socks5h", want: "198.18.5.255:8080"},
	}
	for _, test := range tests {
		t.Run(test.scheme, func(t *testing.T) {
			connectTarget := make(chan string, 1)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = listener.Close() }()
			go func() {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				defer func() { _ = connection.Close() }()
				greeting := make([]byte, 2)
				if _, err := io.ReadFull(connection, greeting); err != nil {
					return
				}
				if _, err := io.ReadFull(connection, make([]byte, greeting[1])); err != nil {
					return
				}
				if _, err := connection.Write([]byte{0x05, 0x00}); err != nil {
					return
				}
				header := make([]byte, 4)
				if _, err := io.ReadFull(connection, header); err != nil {
					return
				}
				var host string
				switch header[3] {
				case 0x01:
					address := make([]byte, net.IPv4len)
					if _, err := io.ReadFull(connection, address); err != nil {
						return
					}
					host = net.IP(address).String()
				case 0x03:
					length := make([]byte, 1)
					if _, err := io.ReadFull(connection, length); err != nil {
						return
					}
					address := make([]byte, int(length[0]))
					if _, err := io.ReadFull(connection, address); err != nil {
						return
					}
					host = string(address)
				default:
					connectTarget <- fmt.Sprintf("unexpected address type %d", header[3])
					return
				}
				portBytes := make([]byte, 2)
				if _, err := io.ReadFull(connection, portBytes); err != nil {
					return
				}
				port := int(portBytes[0])<<8 | int(portBytes[1])
				connectTarget <- net.JoinHostPort(host, strconv.Itoa(port))
				_, _ = connection.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
			}()

			proxyURL := &url.URL{Scheme: test.scheme, Host: listener.Addr().String()}
			transport := newPinnedTransport(&http.Transport{Proxy: http.ProxyURL(proxyURL)}, nil, "", nil,
				func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
					resolved, err := url.Parse(rawURL)
					if err != nil {
						return nil, err
					}
					resolved.Host = "198.18.5.255:8080"
					return resolved, nil
				})
			response, _ := (&http.Client{Timeout: 5 * time.Second, Transport: transport}).Get("http://cdn.example.com:8080/manifest.mpd")
			if response != nil {
				_ = response.Body.Close()
			}

			select {
			case got := <-connectTarget:
				if got != test.want {
					t.Fatalf("SOCKS target = %q, want %q", got, test.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("SOCKS proxy never received a request")
			}
		})
	}
}

func TestPinnedTransportAppliesLocalSSRFValidationBeforeProxying(t *testing.T) {
	proxyURL := &url.URL{Scheme: "http", Host: "127.0.0.1:1"}
	transport := newPinnedTransport(&http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}, nil, "", nil, func(_ context.Context, _ string, _ map[string]struct{}) (*url.URL, error) {
		return nil, fmt.Errorf("resolved to a non-public address")
	})
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	response, err := client.Get("https://internal.example.com/secret")
	if response != nil {
		_ = response.Body.Close()
	}
	if err == nil {
		t.Fatal("the proxy was reached after local SSRF validation failed")
	}
}

func TestPinnedTransportBoundsRebuildsUnderConcurrentReloads(t *testing.T) {
	router, err := NewRouter(Config{Default: Direct})
	if err != nil {
		t.Fatal(err)
	}
	transport := &pinnedTransport{
		router: router,
		pin: func(_ context.Context, rawURL string, _ map[string]struct{}) (*url.URL, error) {
			return url.Parse(rawURL)
		},
		generation: ^uint64(0),
		transports: map[pinnedTransportKey]pinnedTransportEntry{},
	}

	request, err := http.NewRequest(http.MethodGet, "https://cdn.example.com/manifest.mpd", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.RoundTrip(request); err == nil {
		t.Fatal("a permanently stale generation must fail instead of spinning")
	}
}
