//go:build !lite

package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/babywbx/kiln/modules/config"
)

func splitServer(t *testing.T, playRequiresAuth bool) *Server {
	t.Helper()
	cfg := config.File{}
	cfg.Server.Listen = "0.0.0.0:8080"
	cfg.Server.TLSListen = "0.0.0.0:8443"
	cfg.Security.PlayRequireAuth = &playRequiresAuth
	return &Server{deps: Deps{Cfg: cfg}}
}

func TestPlaintextSurfaceKeepsPlaybackAndProbesReachable(t *testing.T) {
	server := splitServer(t, false)
	for _, path := range []string{
		"/healthz", "/readyz", "/metrics",
		"/p/tok/playlist.m3u", "/p/tok/play/news/index.m3u8", "/p/tok/play/news/live/seg1.m4s",
		"/v1/epg.xml", "/v1/epg.xml.gz", "/v1/logo/news",
	} {
		if !server.plaintextSurfaceAllows(path) {
			t.Errorf("%s must stay reachable over plain http or players and health probes break", path)
		}
	}
}

func TestPlaintextSurfaceWithholdsEverythingThatCarriesCredentials(t *testing.T) {
	server := splitServer(t, false)
	for _, path := range []string{
		"/", "/admin", "/admin/channels",
		"/healthz-admin", "/v1/epg.xml/private", "/v1/playlist.m3u/private",
		"/v1/auth/login", "/v1/me", "/v1/me/credentials",
		"/v1/status", "/v1/channels",
		"/v1/admin/settings", "/v1/admin/channels",
	} {
		if server.plaintextSurfaceAllows(path) {
			t.Errorf("%s must not be served in the clear once the console is https-only", path)
		}
	}
}

func TestPlaintextSurfaceFollowsThePlaybackAuthSetting(t *testing.T) {
	open := splitServer(t, false)
	for _, path := range []string{"/v1/play/news/index.m3u8", "/v1/playlist.m3u"} {
		if !open.plaintextSurfaceAllows(path) {
			t.Errorf("%s is public when playback needs no auth, so plain http is fine", path)
		}
	}
	guarded := splitServer(t, true)
	for _, path := range []string{"/v1/play/news/index.m3u8", "/v1/playlist.m3u"} {
		if guarded.plaintextSurfaceAllows(path) {
			t.Errorf("%s carries a bearer token when playback needs auth, so it must not be plain http", path)
		}
	}
}

func TestPlaintextSurfaceRedirectsBrowsersToTheSecurePort(t *testing.T) {
	server := splitServer(t, false)
	handler := server.plaintextSurface(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		"0.0.0.0:8443",
	)
	for host, want := range map[string]string{
		"10.10.5.60:8080": "https://10.10.5.60:8443/admin/channels?filter=news",
		"[2001:db8::1]":   "https://[2001:db8::1]:8443/admin/channels?filter=news",
	} {
		request := httptest.NewRequest(http.MethodGet, "/admin/channels?filter=news", nil)
		request.Host = host
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusPermanentRedirect || response.Header().Get("Location") != want {
			t.Errorf("host %q: status = %d, Location = %q", host, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestPlaintextSurfaceDoesNotRedirectAnUntrustedHost(t *testing.T) {
	server := splitServer(t, false)
	server.deps.Cfg.Security.PublicHosts = []string{"kiln.example"}
	handler := server.plaintextSurface(http.NotFoundHandler(), "0.0.0.0:8443")
	request := httptest.NewRequest(http.MethodGet, "/admin", nil)
	request.Host = "attacker.example:8080"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden || response.Header().Get("Location") != "" {
		t.Fatalf("status = %d, Location = %q", response.Code, response.Header().Get("Location"))
	}
}

func TestPlaintextSurfaceRefusesWritesInsteadOfRedirectingThem(t *testing.T) {
	server := splitServer(t, false)
	handler := server.plaintextSurface(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		"0.0.0.0:8443",
	)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
	request.Host = "10.10.5.60:8080"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; a redirected POST would replay credentials in the clear", response.Code, http.StatusForbidden)
	}
}

func TestPlaintextSurfaceRefusesCredentialPreflights(t *testing.T) {
	server := splitServer(t, false)
	handler := server.plaintextSurface(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }),
		"0.0.0.0:8443",
	)
	request := httptest.NewRequest(http.MethodOptions, "/v1/auth/login", nil)
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; allowing the preflight would make the browser send credentials in cleartext", response.Code, http.StatusForbidden)
	}
}

func TestPlaintextSurfaceDoesNotReplayBearerCredentials(t *testing.T) {
	server := splitServer(t, false)
	handler := server.plaintextSurface(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		"0.0.0.0:8443",
	)
	authorized := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	authorized.Header.Set("Authorization", "Bearer secret")
	for _, request := range []*http.Request{
		authorized,
		httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8?token=secret", nil),
	} {
		request.Host = "10.10.5.60:8080"
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || response.Header().Get("Location") != "" {
			t.Errorf("%s: status = %d, Location = %q", request.URL, response.Code, response.Header().Get("Location"))
		}
	}
}

func TestPlaintextSurfacePassesAllowedRequestsStraightThrough(t *testing.T) {
	server := splitServer(t, false)
	handler := server.plaintextSurface(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }),
		"0.0.0.0:8443",
	)
	request := httptest.NewRequest(http.MethodGet, "/p/tok/playlist.m3u", nil)
	request.Host = "10.10.5.60:8080"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want the wrapped handler to answer", response.Code)
	}
}
