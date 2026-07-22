package staticcatalog

import (
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/config"
)

func TestPlaylistUsesOnlyRequestedActiveChannels(t *testing.T) {
	catalog := New(config.File{
		Server: config.Server{PublicBaseURL: "https://kiln.example"},
		Channels: []config.Channel{
			{ID: "news", Title: "News", Group: "Live"},
			{ID: "sports", Title: "Sports"},
			{ID: "disabled", Disabled: true},
		},
	})

	channels := catalog.FilterByIDs(catalog.List(), []string{"news"})
	body := catalog.Playlist(channels, "signed token")

	for _, want := range []string{
		`#EXTM3U`,
		`group-title="Live"`,
		`https://kiln.example/v1/play/news/index.m3u8?token=signed+token`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("playlist missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "sports") || strings.Contains(body, "disabled") {
		t.Fatalf("playlist contains a filtered channel:\n%s", body)
	}
}

func TestSourceURLSupportsDirectAndConfiguredUpstreams(t *testing.T) {
	catalog := New(config.File{Upstreams: []config.Upstream{{
		ID: "origin", BaseURL: "https://media.example/base/",
	}}})

	direct, err := catalog.SourceURL(config.Channel{SourceURL: "https://direct.example/live.m3u8"})
	if err != nil || direct != "https://direct.example/live.m3u8" {
		t.Fatalf("direct URL = %q, %v", direct, err)
	}
	configured, err := catalog.SourceURL(config.Channel{Upstream: "origin", Path: "live.m3u8"})
	if err != nil || configured != "https://media.example/base/live.m3u8" {
		t.Fatalf("configured URL = %q, %v", configured, err)
	}
}
