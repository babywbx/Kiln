package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
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
