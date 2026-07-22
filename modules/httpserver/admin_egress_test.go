//go:build !lite

package httpserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
	"golang.org/x/net/dns/dnsmessage"
)

func TestNormalizeEgressDraftRejectsDisabledProxyReferences(t *testing.T) {
	tests := []struct {
		name  string
		draft egressDraft
		want  string
	}{
		{
			name: "default",
			draft: egressDraft{
				Default: "proxy", PlaylistPolicy: "rewrite",
				Proxies: []store.ProxyProfileRow{{ID: "proxy", URL: "http://127.0.0.1:8080", Disabled: true}},
			},
			want: "default proxy",
		},
		{
			name: "enabled rule",
			draft: egressDraft{
				Default: "direct", PlaylistPolicy: "rewrite",
				Proxies: []store.ProxyProfileRow{{ID: "proxy", URL: "http://127.0.0.1:8080", Disabled: true}},
				Rules:   []store.ProxyRuleRow{{ID: "rule", Kind: "host_suffix", Pattern: "example.com", ProxyID: "proxy"}},
			},
			want: "references disabled proxy",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := normalizeEgressDraft(test.draft, nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEgressTestWithoutConfiguredRouterMakesARealRequest(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		wantOK bool
	}{
		{name: "http 200 passes", status: http.StatusOK, wantOK: true},
		{name: "http 204 fails", status: http.StatusNoContent, wantOK: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer origin.Close()
			server := &Server{deps: Deps{
				Cfg:     configForEgressTest(),
				Allowed: map[string]struct{}{"127.0.0.1": {}},
			}}
			request := httptest.NewRequest(http.MethodPost, "/v1/admin/egress/test", strings.NewReader(`{"target":"source","url":"`+origin.URL+`"}`))
			request = request.WithContext(context.WithValue(request.Context(), principalKey, requestPrincipal{Kind: "session", Role: "admin"}))
			response := httptest.NewRecorder()
			server.handleAdminEgressTest(response, request)
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || body["ok"] != test.wantOK || body["status"] != float64(test.status) || body["proxy_id"] != "direct" {
				t.Fatalf("response status=%d body=%v", response.Code, body)
			}
		})
	}
}

func TestEgressTestPresetCannotOverrideItsDestination(t *testing.T) {
	originHit := false
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHit = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	server := &Server{deps: Deps{
		Cfg:     configForEgressTest(),
		Allowed: map[string]struct{}{"127.0.0.1": {}},
	}}
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/egress/test", strings.NewReader(`{"target":"bing","url":"`+origin.URL+`"}`))
	ctx, cancel := context.WithCancel(context.WithValue(request.Context(), principalKey, requestPrincipal{Kind: "session", Role: "admin"}))
	cancel()
	request = request.WithContext(ctx)
	response := httptest.NewRecorder()
	server.handleAdminEgressTest(response, request)
	if originHit {
		t.Fatal("trusted preset label was used to reach the caller supplied URL")
	}
}

func TestEgressTestBlocksDNSRebindingBeforeConnecting(t *testing.T) {
	var originHit atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	installRebindingResolver(t, [4]byte{192, 0, 2, 1}, [4]byte{127, 0, 0, 1})

	server := &Server{deps: Deps{Cfg: configForEgressTest()}}
	target := "http://rebind.test:" + originURL.Port()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/egress/test",
		strings.NewReader(`{"target":"custom","url":"`+target+`"}`),
	)
	request = request.WithContext(context.WithValue(
		request.Context(),
		principalKey,
		requestPrincipal{Kind: "session", Role: "admin"},
	))
	response := httptest.NewRecorder()

	server.handleAdminEgressTest(response, request)

	if originHit.Load() {
		t.Fatal("DNS rebinding reached a private address after public validation")
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body["outcome"] != "blocked" {
		t.Fatalf("response status=%d body=%v", response.Code, body)
	}
}

func TestEgressTestPreservesHostWhenPinningAnAllowlistedName(t *testing.T) {
	var receivedHost string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.WriteHeader(http.StatusOK)
	}))
	defer origin.Close()

	originURL, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	installRebindingResolver(t, [4]byte{127, 0, 0, 1}, [4]byte{127, 0, 0, 1})

	server := &Server{deps: Deps{
		Cfg: configForEgressTest(),
		Allowed: map[string]struct{}{
			"probe.test": {},
		},
	}}
	target := "http://probe.test:" + originURL.Port()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/egress/test",
		strings.NewReader(`{"target":"custom","url":"`+target+`"}`),
	)
	request = request.WithContext(context.WithValue(
		request.Context(),
		principalKey,
		requestPrincipal{Kind: "session", Role: "admin"},
	))
	response := httptest.NewRecorder()

	server.handleAdminEgressTest(response, request)

	if response.Code != http.StatusOK || receivedHost != "probe.test:"+originURL.Port() {
		t.Fatalf("response status=%d host=%q body=%s", response.Code, receivedHost, response.Body.String())
	}
}

func TestEgressTestSendsPinnedAddressThroughProxy(t *testing.T) {
	type proxyRequest struct {
		urlHost string
		host    string
	}
	received := make(chan proxyRequest, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- proxyRequest{urlHost: r.URL.Hostname(), host: r.Host}
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	installRebindingResolver(t, [4]byte{127, 0, 0, 1}, [4]byte{127, 0, 0, 1})

	server := &Server{deps: Deps{
		Cfg: configForEgressTest(),
		Allowed: map[string]struct{}{
			"probe.test": {},
		},
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/egress/test",
		strings.NewReader(`{
			"target":"custom",
			"url":"http://probe.test:8080/",
			"proxy_url":"`+proxy.URL+`"
		}`),
	)
	request = request.WithContext(context.WithValue(
		request.Context(),
		principalKey,
		requestPrincipal{Kind: "session", Role: "admin"},
	))
	response := httptest.NewRecorder()

	server.handleAdminEgressTest(response, request)

	var proxied proxyRequest
	select {
	case proxied = <-received:
	case <-time.After(time.Second):
		t.Fatalf("proxy was not reached: status=%d body=%s", response.Code, response.Body.String())
	}
	if response.Code != http.StatusOK || proxied.urlHost != "127.0.0.1" || proxied.host != "127.0.0.1:8080" {
		t.Fatalf("response status=%d proxy request=%#v body=%s", response.Code, proxied, response.Body.String())
	}
}

// net.DefaultResolver is swapped in TestMain, before any test can start a
// lookup. Writing it later races the goroutines the pure-Go resolver leaves in
// flight, which no amount of cleanup ordering can synchronise.
var (
	rebindErr    error
	rebindMu     sync.Mutex
	rebindFirst  [4]byte
	rebindSecond [4]byte
	rebindSeen   = map[string]int{}
)

func TestMain(m *testing.M) {
	startRebindingResolver()
	os.Exit(m.Run())
}

func installRebindingResolver(t *testing.T, first, second [4]byte) {
	t.Helper()
	if rebindErr != nil {
		t.Fatal(rebindErr)
	}
	rebindMu.Lock()
	rebindFirst, rebindSecond = first, second
	rebindSeen = map[string]int{}
	rebindMu.Unlock()
}

func nextRebindingAnswer(name string) [4]byte {
	rebindMu.Lock()
	defer rebindMu.Unlock()
	rebindSeen[name]++
	if rebindSeen[name] > 1 {
		return rebindSecond
	}
	return rebindFirst
}

func startRebindingResolver() {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		rebindErr = err
		return
	}

	go func() {
		buffer := make([]byte, 512)
		for {
			n, address, readErr := conn.ReadFrom(buffer)
			if readErr != nil {
				return
			}
			var parser dnsmessage.Parser
			header, parseErr := parser.Start(buffer[:n])
			if parseErr != nil {
				continue
			}
			questions, parseErr := parser.AllQuestions()
			if parseErr != nil {
				continue
			}
			builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
				ID:                 header.ID,
				Response:           true,
				RecursionDesired:   header.RecursionDesired,
				RecursionAvailable: true,
			})
			if builder.StartQuestions() != nil {
				continue
			}
			for _, question := range questions {
				if builder.Question(question) != nil {
					continue
				}
			}
			if builder.StartAnswers() != nil {
				continue
			}
			for _, question := range questions {
				if question.Type != dnsmessage.TypeA {
					continue
				}
				answer := nextRebindingAnswer(question.Name.String())
				_ = builder.AResource(
					dnsmessage.ResourceHeader{
						Name: question.Name, Class: question.Class, TTL: 0,
					},
					dnsmessage.AResource{A: answer},
				)
			}
			response, buildErr := builder.Finish()
			if buildErr == nil {
				_, _ = conn.WriteTo(response, address)
			}
		}
	}()

	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			return net.Dial("udp", conn.LocalAddr().String())
		},
	}
}

func configForEgressTest() config.File {
	return config.File{Security: config.Security{MaxBodyBytes: 1 << 20}}
}

func TestChannelUpsertRequestKeepsFlatChannelAndEgressFields(t *testing.T) {
	var request channelUpsertRequest
	if err := json.Unmarshal([]byte(`{
		"id":"demo","title":"Demo","source_url":"https://example.com/live.mpd","ingress":"dash",
		"egress":{"mode":"profile","new_proxy":{"name":"HK","url":"socks5h://127.0.0.1:1080"}}
	}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.ID != "demo" || request.Ingress != "dash" || request.Egress == nil || request.Egress.NewProxy == nil || request.Egress.NewProxy.Name != "HK" {
		t.Fatalf("request = %#v", request)
	}
}
