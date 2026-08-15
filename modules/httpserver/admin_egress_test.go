//go:build !lite

package httpserver

import (
	"bytes"
	"compress/gzip"
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

func TestEgressTestReusesRedirectConnectionAndClosesIt(t *testing.T) {
	var connections atomic.Int64
	closed := make(chan struct{}, 1)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			connections.Add(1)
		case http.StateClosed:
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	origin.Start()
	defer origin.Close()

	server := &Server{deps: Deps{
		Cfg:     configForEgressTest(),
		Allowed: map[string]struct{}{"127.0.0.1": {}},
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/egress/test",
		strings.NewReader(`{"target":"source","url":"`+origin.URL+`/redirect"}`),
	)
	request = request.WithContext(context.WithValue(
		request.Context(), principalKey, requestPrincipal{Kind: "session", Role: "admin"},
	))
	response := httptest.NewRecorder()
	server.handleAdminEgressTest(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("response status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want one connection reused across the redirect", got)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("probe connection stayed idle after the handler returned")
	}
}

func TestEgressTestRejectsInterruptedResponse(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
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
	if response.Code != http.StatusOK || body["ok"] != false || body["reachable"] != true || body["outcome"] != "network" {
		t.Fatalf("response status=%d body=%v", response.Code, body)
	}
}

func TestEgressTestMeasuresIdentityBytes(t *testing.T) {
	encoding := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		encoding <- r.Header.Get("Accept-Encoding")
		if r.Header.Get("Accept-Encoding") != "identity" {
			w.Header().Set("Content-Encoding", "gzip")
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			_, _ = writer.Write(bytes.Repeat([]byte("x"), probeSampleBytes))
			_ = writer.Close()
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for compressed.Len() > 0 {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					if _, err := w.Write(compressed.Next(min(128, compressed.Len()))); err != nil {
						return
					}
					if flusher, ok := w.(http.Flusher); ok {
						flusher.Flush()
					}
				}
			}
			return
		}

		chunk := bytes.Repeat([]byte("x"), 16<<10)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if _, err := w.Write(chunk); err != nil {
					return
				}
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}
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
	if got := <-encoding; got != "identity" {
		t.Fatalf("Accept-Encoding = %q, want identity", got)
	}
	if response.Code != http.StatusOK || body["outcome"] != "slow" {
		t.Fatalf("response status=%d body=%v", response.Code, body)
	}
}

func TestEgressTestReturnsDNSOutcomeForInvalidDomain(t *testing.T) {
	server := &Server{deps: Deps{Cfg: configForEgressTest()}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/egress/test",
		strings.NewReader(`{"target":"custom","url":"https://does-not-exist.invalid/"}`),
	)
	request = request.WithContext(context.WithValue(
		request.Context(), principalKey, requestPrincipal{Kind: "session", Role: "admin"},
	))
	response := httptest.NewRecorder()
	server.handleAdminEgressTest(response, request)

	var body struct {
		OK        bool   `json:"ok"`
		Reachable bool   `json:"reachable"`
		Outcome   string `json:"outcome"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.OK || body.Reachable || body.Outcome != "dns" {
		t.Fatalf("response status=%d result=%+v body=%s", response.Code, body, response.Body.String())
	}
}

func TestEgressProbeFailurePrioritizesNetworkErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "dns", err: &url.Error{Op: "Get", URL: "probe target", Err: &net.DNSError{Err: "no such host"}}, want: "dns"},
		{name: "timeout", err: &url.Error{Op: "Get", URL: "probe target", Err: context.DeadlineExceeded}, want: "timeout"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, _ := egressProbeFailure(test.err); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEgressTestPresetCannotOverrideItsDestination(t *testing.T) {
	for _, target := range []string{"public", "bing"} {
		t.Run(target, func(t *testing.T) {
			var originHit atomic.Bool
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHit.Store(true)
				w.WriteHeader(http.StatusNoContent)
			}))
			defer origin.Close()
			server := &Server{deps: Deps{
				Cfg:     configForEgressTest(),
				Allowed: map[string]struct{}{"127.0.0.1": {}},
			}}
			request := httptest.NewRequest(http.MethodPost, "/v1/admin/egress/test", strings.NewReader(`{"target":"`+target+`","url":"`+origin.URL+`"}`))
			ctx, cancel := context.WithCancel(context.WithValue(request.Context(), principalKey, requestPrincipal{Kind: "session", Role: "admin"}))
			cancel()
			request = request.WithContext(ctx)
			response := httptest.NewRecorder()
			server.handleAdminEgressTest(response, request)
			if originHit.Load() {
				t.Fatal("trusted preset label was used to reach the caller supplied URL")
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["target"] != "public" {
				t.Fatalf("target = %v, want public", body["target"])
			}
		})
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
	installRebindingResolver(t, [4]byte{93, 184, 216, 34}, [4]byte{127, 0, 0, 1})

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
		Cfg: configForEgressTest("probe.test"),
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

func TestEgressTestRejectsUnpinnablePlainHTTPProxyTarget(t *testing.T) {
	var reached atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	installRebindingResolver(t, [4]byte{127, 0, 0, 1}, [4]byte{127, 0, 0, 1})

	server := &Server{deps: Deps{
		Cfg: configForEgressTest("probe.test"),
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

	if reached.Load() {
		t.Fatal("proxy was reached for an unpinnable target")
	}
	var body struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.OK || body.Outcome != "blocked" {
		t.Fatalf("response status=%d result=%+v body=%s", response.Code, body, response.Body.String())
	}
}

func TestEgressTestUsesHTTPSAndClassifiesConnectAuthentication(t *testing.T) {
	var reached atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Store(true)
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Proxy-Authenticate", `Basic realm="test"`)
		w.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()
	installRebindingResolver(t, [4]byte{127, 0, 0, 1}, [4]byte{127, 0, 0, 1})

	server := &Server{deps: Deps{
		Cfg: configForEgressTest("example.com"),
	}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/egress/test",
		strings.NewReader(`{"target":"public","proxy_url":"`+proxy.URL+`"}`),
	)
	request = request.WithContext(context.WithValue(
		request.Context(), principalKey, requestPrincipal{Kind: "session", Role: "admin"},
	))
	response := httptest.NewRecorder()
	server.handleAdminEgressTest(response, request)

	var body struct {
		OK      bool   `json:"ok"`
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.OK || body.Outcome != "proxy_auth" || !reached.Load() {
		t.Fatalf("response status=%d result=%+v reached=%t body=%s", response.Code, body, reached.Load(), response.Body.String())
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
			responseCode := dnsmessage.RCodeSuccess
			for _, question := range questions {
				name := "." + strings.TrimSuffix(question.Name.String(), ".") + "."
				if strings.Contains(name, ".invalid.") {
					responseCode = dnsmessage.RCodeNameError
					break
				}
			}
			builder := dnsmessage.NewBuilder(nil, dnsmessage.Header{
				ID:                 header.ID,
				Response:           true,
				RecursionDesired:   header.RecursionDesired,
				RecursionAvailable: true,
				RCode:              responseCode,
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
				if responseCode == dnsmessage.RCodeNameError {
					continue
				}
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

func configForEgressTest(allowedHosts ...string) config.File {
	allowedHosts = append([]string{"127.0.0.1"}, allowedHosts...)
	return config.File{Security: config.Security{MaxBodyBytes: 1 << 20, AllowedHosts: allowedHosts}}
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
