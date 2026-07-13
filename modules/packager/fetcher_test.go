package packager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
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
