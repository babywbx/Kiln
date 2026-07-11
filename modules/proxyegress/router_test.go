package proxyegress

import (
	"net/http"
	"net/url"
	"testing"

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

func TestPreferHTTPSHosts(t *testing.T) {
	u, _ := url.Parse("http://origin.example.com/session/x")
	if !shouldPreferHTTPS(u) {
		t.Fatal("expected prefer https")
	}
	u2, _ := url.Parse("https://origin.example.com/session/x")
	if shouldPreferHTTPS(u2) {
		t.Fatal("already https")
	}
	u3, _ := url.Parse("http://cdn.example.com/v")
	if shouldPreferHTTPS(u3) {
		t.Fatal("unmatched channel should not force https")
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
		t.Fatal("TLSClientConfig required for proxy CDN compat")
	}
	if tr.TLSClientConfig.MinVersion != 0x0303 {
		t.Fatalf("MinVersion want TLS1.2 got %x", tr.TLSClientConfig.MinVersion)
	}
	if tr.TLSClientConfig.MaxVersion != 0x0304 {
		t.Fatalf("MaxVersion want TLS1.3 got %x", tr.TLSClientConfig.MaxVersion)
	}
	if len(tr.TLSClientConfig.NextProtos) != 0 {
		t.Fatalf("NextProtos should be empty for proxy CDN compat, got %v", tr.TLSClientConfig.NextProtos)
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
	env := r.EnvForFFmpeg("http://example.com/", "", true)
	found := false
	for _, e := range env {
		if e == "HTTP_PROXY=socks5h://host.docker.internal:6153" || e == "HTTP_PROXY=socks5://host.docker.internal:6153" {
			found = true
		}
		if contains(e, "host.docker.internal:6153") {
			found = true
		}
	}
	if !found {
		t.Fatalf("env=%v", env)
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
	if env := r.EnvForFFmpeg("http://origin.example.com:8000/live/uhd", "demo-uhd", true); len(env) != 0 {
		t.Fatalf("lan origin env=%v", env)
	}
	// Resolved MPD / BaseURL host must inject docker-rewritten proxy for segment pulls.
	env := r.EnvForFFmpeg("https://origin.example.com/session/x/index.mpd", "demo-uhd", true)
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

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && stringIndex(s, sub) >= 0))
}
func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
