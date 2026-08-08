package packager

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/pull"
)

func TestPullFetcherFetchUsesThePlaylistLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 128))
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := pull.New(pull.Options{
		Allowed:     map[string]struct{}{u.Hostname(): {}},
		MaxPlaylist: 64,
	})
	fetcher := &PullFetcher{Client: client, MaxBytes: 1024}
	if _, _, err := fetcher.Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("manifest larger than the playlist limit succeeded")
	}
}

func TestPullFetcherSendsHeadersOnlyToTheSourceOrigin(t *testing.T) {
	originHeader := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHeader <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "origin")
	}))
	defer origin.Close()

	targetHeader := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHeader <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "target")
	}))
	defer target.Close()

	client := pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	fetcher := NewPullFetcher(client, 1024)(Request{
		SourceURL: origin.URL + "/manifest.mpd",
		Headers:   map[string]string{"X-Channel-Secret": "top-secret"},
	})
	for _, rawURL := range []string{origin.URL + "/segment.m4s", target.URL + "/segment.m4s"} {
		if _, _, err := fetcher.Fetch(context.Background(), rawURL); err != nil {
			t.Fatal(err)
		}
	}
	if got := <-originHeader; got != "top-secret" {
		t.Fatalf("same-origin header = %q, want channel secret", got)
	}
	if got := <-targetHeader; got != "" {
		t.Fatalf("cross-origin header = %q, want empty", got)
	}
}

func TestPullFetcherUsesDefaultDestinationValidation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "private")
	}))
	defer server.Close()

	privateURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/segment.m4s"
	fetcher := NewPullFetcher(pull.New(pull.Options{}), 1024)(Request{SourceURL: privateURL})
	if _, _, err := fetcher.Fetch(context.Background(), privateURL); err == nil {
		t.Fatal("packager fetched a private DNS destination")
	}
}
