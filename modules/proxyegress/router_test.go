package proxyegress

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResolveHostAndChannel(t *testing.T) {
	r, err := NewRouter(Config{
		Default:        Direct,
		PlaylistPolicy: PolicyAuto,
		Profiles: []Profile{
			{ID: "p1", URL: "http://127.0.0.1:7890"},
		},
		Rules: []Rule{
			{Priority: 10, Kind: KindHostSuffix, Pattern: "edge.media.example", ProxyID: "p1"},
			{Priority: 20, Kind: KindChannel, Pattern: "channel-news", ProxyID: "p1"},
			{Priority: 5, Kind: KindHostExact, Pattern: "origin.example.com", ProxyID: Direct},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := r.Resolve("http://primary.edge.media.example/x", "")
	if d.ProxyID != "p1" || !d.Rewrite {
		t.Fatalf("%+v", d)
	}
	d2 := r.Resolve("http://origin.example.com:8000/a", "")
	if d2.ProxyID != Direct || d2.Rewrite {
		t.Fatalf("lan should direct/auto no rewrite %+v", d2)
	}
	d3 := r.Resolve("http://example.com/v", "channel-news")
	if d3.ProxyID != "p1" {
		t.Fatalf("channel rule %+v", d3)
	}
}

func TestRoutingTransportDoesNotRewriteUpstreamURL(t *testing.T) {
	var seen []string
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.String())
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("Please manually redirect to HTTPS."))
	}))
	defer proxySrv.Close()

	r, err := NewRouter(Config{
		Default:  Direct,
		Profiles: []Profile{{ID: "p", URL: proxySrv.URL}},
		Rules:    []Rule{{Priority: 10, Kind: KindHostSuffix, Pattern: "edge.media.example", ProxyID: "p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.ClientForChannel("", "ch", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	const target = "http://primary.edge.media.example:80/session/x/index.mpd"
	resp, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if len(seen) != 1 {
		t.Fatalf("want exactly one upstream request, got %d: %v", len(seen), seen)
	}
	if seen[0] != target {
		t.Fatalf("proxy saw a rewritten URL\n want: %s\n got:  %s", target, seen[0])
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("403 must surface to the caller, got %d", resp.StatusCode)
	}
}

func TestProxyTLSCompat(t *testing.T) {
	c, err := buildClient(&url.URL{Scheme: "http", Host: "127.0.0.1:7890"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if tr.ForceAttemptHTTP2 {
		t.Fatal("proxy client must not force HTTP/2")
	}
	if tr.TLSNextProto == nil {
		t.Fatal("TLSNextProto should be set to disable h2")
	}
	if tr.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig required for proxy compat")
	}
	if tr.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion want TLS1.2 got %x", tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.MaxVersion != 0 {
		t.Fatalf("MaxVersion must stay unpinned, got %x", tr.TLSClientConfig.MaxVersion)
	}
	if len(tr.TLSClientConfig.NextProtos) != 0 {
		t.Fatalf("NextProtos must stay unpinned, got %v", tr.TLSClientConfig.NextProtos)
	}
}

func TestSOCKSNegotiationUsesDialTimeout(t *testing.T) {
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

	client, err := buildClient(
		&url.URL{Scheme: "socks5", Host: listener.Addr().String()},
		25*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	transport := client.Transport.(*http.Transport)
	type result struct {
		connection net.Conn
		err        error
	}
	completed := make(chan result, 1)
	go func() {
		connection, err := transport.DialContext(context.Background(), "tcp", "192.0.2.1:443")
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

func TestPlaylistRewritePolicy(t *testing.T) {
	r, _ := NewRouter(Config{
		Default: "p1", PlaylistPolicy: PolicyRewrite,
		Profiles: []Profile{{ID: "p1", URL: "socks5h://127.0.0.1:6153"}},
	})
	d := r.Resolve("http://example.com/", "")
	if !d.Rewrite || d.ProxyURL == nil {
		t.Fatalf("%+v", d)
	}
}

func TestClientForProxyForcesDirectOrNamedProfile(t *testing.T) {
	r, err := NewRouter(Config{
		Default:  "p1",
		Profiles: []Profile{{ID: "p1", URL: "http://127.0.0.1:7890"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := r.ClientForProxy(Direct, 7*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	directTransport := direct.Transport.(*http.Transport)
	if directTransport.Proxy != nil || direct.Timeout != 7*time.Second {
		t.Fatalf("direct client not fixed direct: %#v", direct)
	}
	proxied, err := r.ClientForProxy("p1", 9*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	proxyTransport := proxied.Transport.(*http.Transport)
	proxyURL, err := proxyTransport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil || proxyURL.String() != "http://127.0.0.1:7890" || proxied.Timeout != 9*time.Second {
		t.Fatalf("named proxy client = url %v timeout %v err %v", proxyURL, proxied.Timeout, err)
	}
	if _, err := r.ClientForProxy("missing", time.Second); err == nil {
		t.Fatal("unknown proxy unexpectedly accepted")
	}
}

func TestResolveUsesCDNRouteNotLANOrigin(t *testing.T) {
	r, err := NewRouter(Config{
		Default:        Direct,
		PlaylistPolicy: PolicyRewrite,
		Profiles:       []Profile{{ID: "local-http", URL: "http://127.0.0.1:7890"}},
		Rules: []Rule{
			{Priority: 5, Kind: KindHostExact, Pattern: "origin.example.com", ProxyID: Direct},
			{Priority: 10, Kind: KindHostSuffix, Pattern: "edge.media.example", ProxyID: "local-http"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d := r.Resolve("http://origin.example.com:8000/live/uhd", "channel-uhd"); d.ProxyID != Direct {
		t.Fatalf("lan origin proxy = %q, want direct", d.ProxyID)
	}
	d := r.Resolve("https://primary.edge.media.example/session/x/index.mpd", "channel-uhd")
	if d.ProxyID != "local-http" || d.ProxyURL == nil || d.ProxyURL.String() != "http://127.0.0.1:7890" {
		t.Fatalf("cdn decision = %+v", d)
	}
}

func TestNewRouterRejectsInvalidActiveRules(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unknown kind",
			cfg: Config{Rules: []Rule{{
				ID: "bad-kind", Kind: RuleKind("typo"), Pattern: "example.com", ProxyID: Direct,
			}}},
			want: "invalid kind",
		},
		{
			name: "invalid regex",
			cfg: Config{Rules: []Rule{{
				ID: "bad-regex", Kind: KindHostRegex, Pattern: "[", ProxyID: Direct,
			}}},
			want: "pattern",
		},
		{
			name: "disabled profile",
			cfg: Config{
				Profiles: []Profile{{ID: "disabled", URL: "http://127.0.0.1:7890", Disabled: true}},
				Rules: []Rule{{
					ID: "disabled-profile", Kind: KindHostExact, Pattern: "example.com", ProxyID: "disabled",
				}},
			},
			want: "unknown or disabled proxy",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewRouter(test.cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewRouter() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestTransportForUsesOneReloadSnapshot(t *testing.T) {
	configs := []Config{
		{Default: "p", Profiles: []Profile{{ID: "p", URL: "http://proxy-a:1001"}}},
		{Default: "p", Profiles: []Profile{{ID: "p", URL: "http://proxy-b:1002"}}},
	}
	router, err := NewRouter(configs[0])
	if err != nil {
		t.Fatal(err)
	}

	const iterations = 5000
	errors := make(chan error, 1)
	report := func(err error) {
		select {
		case errors <- err:
		default:
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			if err := router.Reload(configs[i%len(configs)]); err != nil {
				report(err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			const target = "https://example.com/live.mpd"
			d, transport, _, err := router.transportFor(target, "")
			if err != nil {
				report(err)
				return
			}
			if d.ProxyURL == nil {
				report(fmt.Errorf("mixed reload snapshot produced no proxy"))
				return
			}
			proxy := d.ProxyURL.String()
			if proxy != "http://proxy-a:1001" && proxy != "http://proxy-b:1002" {
				report(fmt.Errorf("mixed reload snapshot produced %q", proxy))
				return
			}
			request, _ := http.NewRequest(http.MethodGet, target, nil)
			transportProxy, err := transport.Proxy(request)
			if err != nil || transportProxy == nil || transportProxy.String() != proxy {
				report(fmt.Errorf("decision proxy %q used transport proxy %v: %w", proxy, transportProxy, err))
				return
			}
		}
	}()
	wg.Wait()
	close(errors)
	if err := <-errors; err != nil {
		t.Fatal(err)
	}
}

func TestDecisionMarksProxyResolvesOnlyForTrustedProxies(t *testing.T) {
	profiles := []Profile{{ID: "hk", URL: "http://proxy.example:1080"}}
	for _, test := range []struct {
		name    string
		cfg     Config
		target  string
		want    bool
		wantVia string
	}{
		{
			name: "trusted proxy", want: true, wantVia: "hk", target: "https://cdn.example/a.mpd",
			cfg: Config{Default: "hk", Profiles: profiles, TrustProxyDNS: true},
		},
		{
			name: "untrusted proxy", want: false, wantVia: "hk", target: "https://cdn.example/a.mpd",
			cfg: Config{Default: "hk", Profiles: profiles},
		},
		{
			name: "direct stays pinned", want: false, wantVia: Direct, target: "https://cdn.example/a.mpd",
			cfg: Config{Default: Direct, Profiles: profiles, TrustProxyDNS: true},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			router, err := NewRouter(test.cfg)
			if err != nil {
				t.Fatal(err)
			}
			decision := router.Resolve(test.target, "")
			if decision.ProxyID != test.wantVia {
				t.Fatalf("proxy = %q, want %q", decision.ProxyID, test.wantVia)
			}
			if decision.ProxyResolves != test.want {
				t.Fatalf("ProxyResolves = %v, want %v", decision.ProxyResolves, test.want)
			}
		})
	}
}
