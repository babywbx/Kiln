package epg_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

func TestFetchLogoFallsBackInPriorityOrder(t *testing.T) {
	t.Parallel()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/gitee.png" {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-data"))
	}))
	t.Cleanup(server.Close)

	result, err := epg.FetchLogo(context.Background(), server.Client(), []epg.LogoCandidate{
		{SourceID: "gitee", URL: server.URL + "/gitee.png", Priority: 1},
		{SourceID: "github", URL: server.URL + "/github.png", Priority: 2},
	}, 1024)
	if err != nil {
		t.Fatalf("FetchLogo: %v", err)
	}
	if result.SourceID != "github" || result.ContentType != "image/png" || string(result.Data) != "png-data" || requests != 2 {
		t.Fatalf("result = %#v, requests = %d", result, requests)
	}
}

func TestFetchLogoRejectsOversizedAndNonImageResponses(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/text" {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("not an image"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(make([]byte, 2048))
	}))
	t.Cleanup(server.Close)

	_, err := epg.FetchLogo(context.Background(), server.Client(), []epg.LogoCandidate{
		{SourceID: "text", URL: server.URL + "/text"},
		{SourceID: "large", URL: server.URL + "/large"},
	}, 1024)
	if err == nil {
		t.Fatal("invalid logo responses were accepted")
	}
}
