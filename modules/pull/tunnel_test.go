package pull

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/proxyegress"
)

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

func TestDialPinnedRejectsSOCKSProxy(t *testing.T) {
	router, err := proxyegress.NewRouter(proxyegress.Config{
		Default: "socks",
		Profiles: []proxyegress.Profile{{
			ID: "socks", URL: "socks5://127.0.0.1:1080",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := New(Options{
		Allowed: map[string]struct{}{"127.0.0.1": {}},
		Router:  router,
	})
	if _, err := client.DialPinned(context.Background(), "https://127.0.0.1:443", "channel"); err == nil {
		t.Fatal("SOCKS route was accepted for an FFmpeg CONNECT tunnel")
	}
}
