package proxyegress

import (
	"crypto/tls"
	"fmt"
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
			{Priority: 10, Kind: KindHostSuffix, Pattern: "edge.media.example", ProxyID: "local-http"},
		},
		DockerProxyHost: "host.docker.internal",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := r.EnvForFFmpeg("http://origin.example.com:8000/live/uhd", "channel-uhd", true)
	if err != nil || len(env) != 0 {
		t.Fatalf("lan origin env=%v err=%v", env, err)
	}
	env, err = r.EnvForFFmpeg("https://primary.edge.media.example/session/x/index.mpd", "channel-uhd", true)
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

func TestEnvForFFmpegUsesOneReloadSnapshot(t *testing.T) {
	configs := []Config{
		{
			Default: "p", Profiles: []Profile{{ID: "p", URL: "http://127.0.0.1:1001"}},
			DockerProxyHost: "proxy-a",
		},
		{
			Default: "p", Profiles: []Profile{{ID: "p", URL: "http://127.0.0.1:1002"}},
			DockerProxyHost: "proxy-b",
		},
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
			env, err := router.EnvForFFmpeg("https://example.com/live.mpd", "", true)
			if err != nil {
				report(err)
				return
			}
			var proxy string
			for _, value := range env {
				if strings.HasPrefix(value, "HTTP_PROXY=") {
					proxy = strings.TrimPrefix(value, "HTTP_PROXY=")
					break
				}
			}
			if proxy != "http://proxy-a:1001" && proxy != "http://proxy-b:1002" {
				report(fmt.Errorf("mixed reload snapshot produced %q", proxy))
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
