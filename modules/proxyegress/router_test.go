package proxyegress

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func TestEmptyStoreDoesNotFallBackToFileEgress(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SeedFromConfig(config.File{Egress: config.Egress{Default: Direct, PlaylistPolicy: string(PolicyRewrite)}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigFromStore(db, config.File{
		Proxies: []config.ProxyProfile{{ID: "file-proxy", URL: "http://127.0.0.1:8080"}},
		Egress:  config.Egress{Default: "file-proxy", Rules: []config.EgressRule{{ID: "file-rule", Proxy: "file-proxy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 || len(cfg.Rules) != 0 {
		t.Fatalf("empty authoritative store fell back to file: %#v", cfg)
	}
}

func TestResolveHostAndChannel(t *testing.T) {
	r, err := NewRouter(Config{
		Default:        Direct,
		PlaylistPolicy: PolicyAuto,
		Profiles: []Profile{
			{ID: "p1", URL: "http://127.0.0.1:7890"},
		},
		Rules: []Rule{
			{Priority: 10, Kind: KindHostSuffix, Pattern: "origin.example.com", ProxyID: "p1"},
			{Priority: 20, Kind: KindChannel, Pattern: "channel-news", ProxyID: "p1"},
			{Priority: 5, Kind: KindHostExact, Pattern: "origin.example.com", ProxyID: Direct},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := r.Resolve("http://origin.example.com/x", "")
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

// Upstream URLs must reach the proxy byte-for-byte. Rewriting http→https for
// a CDN broke DASH: the edge served the manifest but 403'd every segment.
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
		Rules:    []Rule{{Priority: 10, Kind: KindHostSuffix, Pattern: "origin.example.com", ProxyID: "p"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, err := r.ClientForChannel("", "ch", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	const target = "http://origin.example.com/session/x/index.mpd"
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
	// Pinning MaxVersion or ALPN caused handshake failures against some edges.
	if tr.TLSClientConfig.MaxVersion != 0 {
		t.Fatalf("MaxVersion must stay unpinned, got %x", tr.TLSClientConfig.MaxVersion)
	}
	if len(tr.TLSClientConfig.NextProtos) != 0 {
		t.Fatalf("NextProtos must stay unpinned, got %v", tr.TLSClientConfig.NextProtos)
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

// ffmpeg cannot use a SOCKS proxy. Handing it one used to emit an http_proxy it
// silently ignored, fetching the media direct — report the misroute instead.
func TestEnvForFFmpegRejectsSocksRoute(t *testing.T) {
	r, _ := NewRouter(Config{
		Default:  "p1",
		Profiles: []Profile{{ID: "p1", URL: "socks5h://127.0.0.1:6153"}},
	})
	env, err := r.EnvForFFmpeg("http://example.com/", "", true)
	if err == nil {
		t.Fatalf("want error for socks route, got env=%v", env)
	}
	if env != nil {
		t.Fatalf("no env may be emitted for an unusable route, got %v", env)
	}
}

func TestEnvForFFmpegUsesCDNHostNotLANOrigin(t *testing.T) {
	r, err := NewRouter(Config{
		Default:        Direct,
		PlaylistPolicy: PolicyRewrite,
		Profiles:       []Profile{{ID: "local-http", URL: "http://127.0.0.1:7890"}},
		Rules: []Rule{
			{Priority: 5, Kind: KindHostExact, Pattern: "origin.example.com", ProxyID: Direct},
			{Priority: 10, Kind: KindHostSuffix, Pattern: "origin.example.com", ProxyID: "local-http"},
		},
		DockerProxyHost: "host.docker.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	// LAN origin alone must not inject a proxy (would miss CDN host rules).
	env, err := r.EnvForFFmpeg("http://origin.example.com:8000/live/uhd", "demo-uhd", true)
	if err != nil || len(env) != 0 {
		t.Fatalf("lan origin env=%v err=%v", env, err)
	}
	// Resolved MPD / BaseURL host must inject docker-rewritten proxy for segment pulls.
	env, err = r.EnvForFFmpeg("https://origin.example.com/session/x/index.mpd", "demo-uhd", true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range env {
		if e == "HTTP_PROXY=http://host.docker.internal:7890" {
			found = true
		}
	}
	if !found {
		t.Fatalf("cdn env=%v", env)
	}
}
