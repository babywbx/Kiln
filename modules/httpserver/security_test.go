//go:build !lite

package httpserver

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/observe"
)

func TestRedactRequestPathRemovesDistributionToken(t *testing.T) {
	token, err := accesstoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw := "/p/" + token + "/play/news/index.m3u8"
	got := redactRequestPath(raw)
	if strings.Contains(got, token) {
		t.Fatalf("redacted path leaked token: %q", got)
	}
	want := "/p/" + accesstoken.Prefix(token) + "…/play/news/index.m3u8"
	if got != want {
		t.Fatalf("redacted path = %q, want %q", got, want)
	}
}

func TestRedactRequestPathLeavesOtherPathsAlone(t *testing.T) {
	const path = "/v1/admin/channels/news"
	if got := redactRequestPath(path); got != path {
		t.Fatalf("path = %q", got)
	}
}

func TestRedactRequestPathRemovesEncodedUpstream(t *testing.T) {
	const upstream = "aHR0cHM6Ly9vcmlnaW4uZXhhbXBsZS9saXZlLnRzP3Rva2VuPXNlY3JldCZzaWc9c2lnbmF0dXJl"
	token, err := accesstoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"direct":       "/u/[redacted]",
		"channel":      "/v1/play/news/u/[redacted]",
		"distribution": "/p/" + accesstoken.Prefix(token) + "…/play/news/u/[redacted]",
	}
	paths := map[string]string{
		"direct":       "/u/" + upstream,
		"channel":      "/v1/play/news/u/" + upstream,
		"distribution": "/p/" + token + "/play/news/u/" + upstream,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			got := redactRequestPath(paths[name])
			if got != want || strings.Contains(got, upstream) {
				t.Fatalf("redacted path = %q, want %q", got, want)
			}
		})
	}
}

func TestRequestLogsRedactEncodedUpstream(t *testing.T) {
	const upstream = "aHR0cHM6Ly9vcmlnaW4uZXhhbXBsZS9saXZlLnRzP3Rva2VuPXNlY3JldCZzaWc9c2lnbmF0dXJl"
	var output bytes.Buffer
	server := &Server{deps: Deps{
		Observe: observe.New(),
		Log:     slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}}

	badRequest := server.withMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/play/news/u/"+upstream+"?token=raw-player-secret", nil)
	badRequest.ServeHTTP(httptest.NewRecorder(), request)

	distributionToken, err := accesstoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	panicking := server.withMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("fixture panic")
	}))
	request = httptest.NewRequest(http.MethodGet, "/p/"+distributionToken+"/play/news/u/"+upstream+"?sig=raw-signature", nil)
	panicking.ServeHTTP(httptest.NewRecorder(), request)

	logs := output.String()
	if strings.Contains(logs, upstream) || strings.Contains(logs, distributionToken) ||
		strings.Contains(logs, "raw-player-secret") || strings.Contains(logs, "raw-signature") {
		t.Fatalf("request logs leaked secret path or query: %s", logs)
	}
	if !strings.Contains(logs, "path=/v1/play/news/u/[redacted]") ||
		!strings.Contains(logs, "path=/p/"+accesstoken.Prefix(distributionToken)+"…/play/news/u/[redacted]") ||
		!strings.Contains(logs, "status=400") || !strings.Contains(logs, "status=500") {
		t.Fatalf("request logs did not retain safe diagnostics: %s", logs)
	}
}

func TestPublicURLRemovesCredentialsAndQuery(t *testing.T) {
	got := publicURL("https://user:secret@example.com/live/index.m3u8?token=abc&quality=4k", false)
	if got != "https://example.com/live/index.m3u8" {
		t.Fatalf("public url = %q", got)
	}
	host := publicURL("socks5://user:secret@127.0.0.1:6153", true)
	if host != "socks5://127.0.0.1:6153" {
		t.Fatalf("public proxy url = %q", host)
	}
}
