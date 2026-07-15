package catalog

import (
	"errors"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func TestM3UImportUsesDirectSourceURLsAndKeepsAdvancedSettings(t *testing.T) {
	cfg := config.File{Channels: []config.Channel{{
		ID:             "demo",
		Title:          "Old Demo",
		SourceURL:      "https://old.example/live.m3u8",
		Ingress:        "hls",
		OnDemand:       true,
		IdleTimeoutSec: 123,
		MaxViewers:     12,
		Headers:        map[string]string{"Authorization": "Bearer retained-secret"},
	}}}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}

	svc := New(cfg, db)
	raw := `#EXTM3U
#EXTINF:-1 tvg-id="demo" tvg-name="Demo Guide",Imported Demo
https://cdn.example/live/index.m3u8?token=abc
#EXTINF:-1 tvg-id="new-channel" group-title="News",New Channel
https://edge.example/channel/master.m3u8?auth=x
#EXTINF:-1 tvg-id="dash-without-keys",DASH Without Keys
https://edge.example/channel/manifest.mpd?auth=x
#EXTINF:-1 tvg-id="invalid",Invalid URL
ftp://edge.example/channel.m3u8
`

	preview, err := svc.PreviewM3U(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Preview || preview.Count != 4 || preview.Created != 1 || preview.Updated != 1 || preview.Skipped != 2 {
		t.Fatalf("preview = %#v", preview)
	}
	if got := preview.Entries[0]; got.Action != ImportUpdate || got.URL != "https://cdn.example/live/index.m3u8?token=abc" || got.SuggestedIngress != "hls" {
		t.Fatalf("updated entry = %#v", got)
	}
	if got := preview.Entries[1]; got.Action != ImportCreate || got.URL != "https://edge.example/channel/master.m3u8?auth=x" {
		t.Fatalf("created entry = %#v", got)
	}
	if got := preview.Entries[2]; got.Action != ImportSkip || !got.Skip || got.SuggestedIngress != "dash" {
		t.Fatalf("dash entry = %#v", got)
	}
	if got := preview.Entries[3]; got.Action != ImportSkip || !got.Skip {
		t.Fatalf("invalid entry = %#v", got)
	}

	before, ok, err := db.GetChannelRow("demo")
	if err != nil || !ok {
		t.Fatalf("existing row: found=%v err=%v", ok, err)
	}
	applied, err := svc.ApplyM3U(raw, map[string]int64{"demo": before.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.Created != 1 || applied.Updated != 1 || applied.Skipped != 2 {
		t.Fatalf("applied = %#v", applied)
	}

	updated, ok, err := db.GetChannelRow("demo")
	if err != nil || !ok {
		t.Fatalf("updated row: found=%v err=%v", ok, err)
	}
	if updated.Channel.SourceURL != "https://cdn.example/live/index.m3u8?token=abc" || updated.Channel.Upstream != "" || updated.Channel.Path != "" {
		t.Fatalf("updated source = %#v", updated.Channel)
	}
	if updated.Channel.MaxViewers != 12 || updated.Channel.IdleTimeoutSec != 123 || updated.Channel.Headers["Authorization"] != "Bearer retained-secret" {
		t.Fatalf("advanced settings were not retained: %#v", updated.Channel)
	}
	created, ok, err := db.GetChannelRow("new-channel")
	if err != nil || !ok || created.Channel.SourceURL != "https://edge.example/channel/master.m3u8?auth=x" {
		t.Fatalf("created row = %#v, found=%v err=%v", created, ok, err)
	}

	if _, err := svc.ApplyM3U(raw, map[string]int64{"demo": before.Revision}); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale apply error = %v", err)
	}
}
