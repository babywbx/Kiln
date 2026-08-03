package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func TestM3UAdvertisesRewrittenEPG(t *testing.T) {
	svc := New(config.File{}, nil)
	body := svc.M3U([]config.Channel{{
		ID: "channel-news", Title: "News Channel", Group: "News",
		LogoURL: "https://logo.example/news.png", EPGID: "source-news", EPGName: "News Channel",
	}}, "https://kiln.example", "/v1/play/", "token", "https://kiln.example/v1/epg.xml.gz")

	wants := []string{
		`#EXTM3U x-tvg-url="https://kiln.example/v1/epg.xml.gz"`,
		`tvg-id="channel-news"`,
		`tvg-name="News Channel"`,
		`tvg-logo="https://logo.example/news.png"`,
		`group-title="News"`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("M3U missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `tvg-id="source-news"`) {
		t.Fatalf("M3U leaked source EPG id instead of Kiln id:\n%s", body)
	}
}

func TestSourceURLPrefersDirectURLAndKeepsLegacyUpstreams(t *testing.T) {
	svc := New(config.File{Upstreams: []config.Upstream{{
		ID: "origin", BaseURL: "https://legacy.example/base",
	}}}, nil)

	direct := config.Channel{
		SourceURL: "https://media.example/live/index.m3u8?token=abc",
		Upstream:  "origin",
		Path:      "/stale.m3u8",
	}
	got, err := svc.SourceURL(direct)
	if err != nil || got != direct.SourceURL {
		t.Fatalf("direct source URL = %q, %v", got, err)
	}
	if upstream, err := svc.Upstream(direct); err != nil || upstream.ID != "" || upstream.BaseURL != "" || len(upstream.Headers) != 0 {
		t.Fatalf("direct source upstream = %#v, %v", upstream, err)
	}

	legacy := config.Channel{Upstream: "origin", Path: "/live/index.m3u8"}
	got, err = svc.SourceURL(legacy)
	if err != nil || got != "https://legacy.example/base/live/index.m3u8" {
		t.Fatalf("legacy source URL = %q, %v", got, err)
	}
}

func TestUpsertChannelAcceptsDirectSourceURL(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := New(config.File{}, db)
	want := config.Channel{
		ID: "direct", SourceURL: "https://media.example/live/index.m3u8", Ingress: "hls", OnDemand: true,
	}
	if err := svc.Upsert(want); err != nil {
		t.Fatalf("upsert direct source: %v", err)
	}
	got, ok := svc.GetAny(want.ID)
	if !ok || got.SourceURL != want.SourceURL || got.Upstream != "" || got.Path != "" {
		t.Fatalf("stored channel = %#v, found=%v", got, ok)
	}
}

func TestUpsertDASHChannelUsesGlobalKeys(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "kiln.toml")
	if err := os.WriteFile(filepath.Join(dir, "kiln.keys"), []byte(
		"00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`
[server]
data_dir = "./data"

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"

[packager]
keys_file = "kiln.keys"
`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := New(cfg, db)

	want := config.Channel{
		ID: "dash", SourceURL: "https://media.example/live.mpd", Ingress: "dash", OnDemand: true,
	}
	if err := svc.Upsert(want); err != nil {
		t.Fatalf("upsert DASH with global keys: %v", err)
	}
}

func TestPublicChannelViewsDoNotExposeSourceCredentials(t *testing.T) {
	svc := New(config.File{Channels: []config.Channel{{
		ID: "private", Title: "Private", SourceURL: "https://user:pass@origin.example/live.m3u8?token=secret", Ingress: "hls",
	}}}, nil)
	views, err := svc.ListViews("https://kiln.example", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].SourceURL != "" || views[0].Upstream != "" || views[0].Path != "" {
		t.Fatalf("public channel leaked source fields: %+v", views)
	}
	adminViews, err := svc.ListViews("https://kiln.example", true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminViews) != 1 || adminViews[0].SourceURL == "" {
		t.Fatalf("admin channel lost editable source: %+v", adminViews)
	}
}

func TestActiveChannelsPrefersStore(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	cfg := config.File{Channels: []config.Channel{{
		ID: "cfg-only", SourceURL: "https://one.example/live.m3u8",
		Ingress: "hls", Autostart: true,
	}}}
	svc := New(cfg, db)
	if err := svc.Upsert(config.Channel{
		ID: "db-only", SourceURL: "https://two.example/live.m3u8",
		Ingress: "hls", Autostart: true, OnDemand: true,
	}); err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, ch := range svc.ActiveChannels() {
		ids[ch.ID] = true
	}
	if !ids["db-only"] || ids["cfg-only"] {
		t.Fatalf("store-backed active channels = %v", ids)
	}

	static := New(cfg, nil)
	channels := static.ActiveChannels()
	if len(channels) != 1 || channels[0].ID != "cfg-only" {
		t.Fatalf("config-backed active channels = %+v", channels)
	}
}
