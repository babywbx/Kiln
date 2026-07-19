package store_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func TestChannelTrackSelectionRoundTrip(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	want := config.TrackSelection{
		Video:     config.VideoSelection{Mode: "cap", Track: config.TrackSelector{Key: "trk-video", RepresentationID: "v1080-50", Height: 1080, FrameRate: "50"}},
		Audio:     config.AudioSelection{Mode: "prefer", Track: config.TrackSelector{Key: "trk-audio", RepresentationID: "a-yue", Language: "yue"}, PreferredLanguages: []string{"yue", "zh"}},
		Subtitles: config.SubtitleSelection{Mode: "off"},
	}
	channel := config.Channel{ID: "selection", Title: "Selection", SourceURL: "https://example.com/live.m3u8", Ingress: "hls", OnDemand: true, Selection: want}
	if err := db.UpsertChannel(channel); err != nil {
		t.Fatal(err)
	}
	got, found, err := db.GetChannel("selection")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(got.Selection, want) {
		t.Fatalf("selection = %#v, want %#v", got.Selection, want)
	}
}

func TestOpenMigratesV1WithoutLosingData(t *testing.T) {
	dir := t.TempDir()
	createV1Database(t, dir)

	db, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open v1 database: %v", err)
	}

	channels, err := db.ListChannels(true)
	if err != nil {
		t.Fatalf("list migrated channels: %v", err)
	}
	if len(channels) != 2 {
		t.Fatalf("channel count = %d, want 2", len(channels))
	}
	if channels[0].ID != "default-agent" || channels[0].UserAgent != "" {
		t.Fatalf("default user agent channel = %#v, want empty user agent", channels[0])
	}
	if channels[1].ID != "custom-agent" || channels[1].UserAgent != "Custom/7" {
		t.Fatalf("custom user agent channel = %#v", channels[1])
	}
	if channels[0].SourceURL != "" || channels[1].SourceURL != "" {
		t.Fatalf("legacy channels gained source URLs: %#v", channels)
	}

	tokens, err := db.ListAccessTokens()
	if err != nil {
		t.Fatalf("list migrated tokens: %v", err)
	}
	if len(tokens) != 1 || tokens[0].ID != "legacy-token" || tokens[0].ExpiresAt != 0 {
		t.Fatalf("migrated tokens = %#v", tokens)
	}

	setting, ok, err := db.GetSetting("public_base_url")
	if err != nil || !ok || setting != "https://kiln.example" {
		t.Fatalf("migrated setting = %q, %v, %v", setting, ok, err)
	}
	profiles, err := db.ListProxyProfiles()
	if err != nil || len(profiles) != 1 || profiles[0].ID != "proxy-1" {
		t.Fatalf("migrated profiles = %#v, %v", profiles, err)
	}
	if err := db.SeedFromConfig(config.File{Egress: config.Egress{Default: "proxy-1", PlaylistPolicy: "rewrite"}}); err != nil {
		t.Fatalf("seed settings around migrated profiles: %v", err)
	}
	if value, ok, err := db.GetSetting("egress_default"); err != nil || !ok || value != "proxy-1" {
		t.Fatalf("migrated egress revision anchor = %q, found=%v err=%v", value, ok, err)
	}
	rules, err := db.ListProxyRules()
	if err != nil || len(rules) != 1 || rules[0].ID != "rule-1" {
		t.Fatalf("migrated rules = %#v, %v", rules, err)
	}
	logs, err := db.ListAccessLogs(10, "")
	if err != nil || len(logs) != 1 || logs[0].ID != 1 {
		t.Fatalf("migrated logs = %#v, %v", logs, err)
	}
	if logs[0].Path != "/p/kiln_old…/playlist.m3u" {
		t.Fatalf("migrated log path = %q", logs[0].Path)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close migrated database: %v", err)
	}
	db, err = store.Open(dir)
	if err != nil {
		t.Fatalf("reopen migrated database: %v", err)
	}
	defer db.Close()

	tokens, err = db.ListAccessTokens()
	if err != nil || len(tokens) != 1 || tokens[0].ID != "legacy-token" {
		t.Fatalf("tokens after reopen = %#v, %v", tokens, err)
	}
}

func TestAccessTokenExpiryRoundTrip(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	want := store.AccessTokenRow{
		ID: "expiring", Name: "Temporary", TokenHash: "hash-expiring", Prefix: "kiln_abc",
		ScopeJSON: `{"channels":["news"]}`, Enabled: true, CreatedAt: 100, ExpiresAt: 200,
	}
	if err := db.InsertAccessToken(want); err != nil {
		t.Fatalf("insert token: %v", err)
	}

	got, ok, err := db.GetAccessTokenByHash(want.TokenHash)
	if err != nil || !ok {
		t.Fatalf("get token: found=%v err=%v", ok, err)
	}
	if got.ExpiresAt != want.ExpiresAt {
		t.Fatalf("expires_at = %d, want %d", got.ExpiresAt, want.ExpiresAt)
	}
}

func TestTouchAccessTokenCoalescesRecentWrites(t *testing.T) {
	t.Parallel()

	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now().Unix()
	recent := store.AccessTokenRow{
		ID: "recent", Name: "Recent", TokenHash: "recent-hash", Prefix: "kiln_recent",
		ScopeJSON: "{}", Enabled: true, CreatedAt: now - 100, LastUsedAt: now - 10,
	}
	stale := store.AccessTokenRow{
		ID: "stale", Name: "Stale", TokenHash: "stale-hash", Prefix: "kiln_stale",
		ScopeJSON: "{}", Enabled: true, CreatedAt: now - 200, LastUsedAt: now - 120,
	}
	if err := db.InsertAccessToken(recent); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertAccessToken(stale); err != nil {
		t.Fatal(err)
	}

	if err := db.TouchAccessToken(recent.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.TouchAccessToken(stale.ID); err != nil {
		t.Fatal(err)
	}

	rows, err := db.ListAccessTokens()
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]int64, len(rows))
	for _, row := range rows {
		got[row.ID] = row.LastUsedAt
	}
	if got[recent.ID] != recent.LastUsedAt {
		t.Fatalf("recent last_used_at = %d, want unchanged %d", got[recent.ID], recent.LastUsedAt)
	}
	if got[stale.ID] < now {
		t.Fatalf("stale last_used_at = %d, want at least %d", got[stale.ID], now)
	}
}

func TestChannelEgressBindingIsAtomicAndKeepsAdvancedRules(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SeedFromConfig(config.File{Egress: config.Egress{Default: "direct", PlaylistPolicy: "rewrite"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProxyProfile(store.ProxyProfileRow{ID: "existing", Name: "Existing", URL: "http://127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertProxyRule(store.ProxyRuleRow{ID: "advanced", Priority: 50, Kind: "channel_id", Pattern: "demo", ProxyID: "direct"}); err != nil {
		t.Fatal(err)
	}
	channel := config.Channel{ID: "demo", Title: "Demo", SourceURL: "https://example.com/live.m3u8", Ingress: "hls", OnDemand: true}
	if err := db.UpsertChannelWithEgress(channel, 0, store.ChannelEgressBinding{Mode: "profile", ProfileID: "existing"}); err != nil {
		t.Fatal(err)
	}
	rules, err := db.ListProxyRules()
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(rules, store.ManagedChannelRuleID("demo"), "existing") || !hasRule(rules, "advanced", "direct") {
		t.Fatalf("rules = %#v", rules)
	}
	row, found, err := db.GetChannelRow("demo")
	if err != nil || !found {
		t.Fatalf("channel found=%v err=%v", found, err)
	}
	channel.Title = "Should roll back"
	duplicate := store.ProxyProfileRow{ID: "existing", URL: "http://127.0.0.1:9999"}
	if err := db.UpsertChannelWithEgress(channel, row.Revision, store.ChannelEgressBinding{Mode: "profile", NewProfile: &duplicate}); err == nil {
		t.Fatal("duplicate quick profile unexpectedly succeeded")
	}
	rolledBack, _, err := db.GetChannelRow("demo")
	if err != nil || rolledBack.Channel.Title != "Demo" {
		t.Fatalf("partial channel update = %#v, err=%v", rolledBack, err)
	}
	if err := db.UpsertChannelWithEgress(rolledBack.Channel, rolledBack.Revision, store.ChannelEgressBinding{Mode: "auto"}); err != nil {
		t.Fatal(err)
	}
	rules, _ = db.ListProxyRules()
	if hasRule(rules, store.ManagedChannelRuleID("demo"), "existing") || !hasRule(rules, "advanced", "direct") {
		t.Fatalf("automatic mode changed advanced rules: %#v", rules)
	}
}

func hasRule(rules []store.ProxyRuleRow, id, proxyID string) bool {
	for _, rule := range rules {
		if rule.ID == id && rule.ProxyID == proxyID {
			return true
		}
	}
	return false
}

func TestAdminAPITokenLifecycleAndAudit(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	row := store.AdminAPITokenRow{
		ID: "admin-token", Name: "Automation", TokenHash: "hash", Prefix: "kiln_v1_12345678",
		ScopeJSON: `["read","refresh"]`, Enabled: true, CreatedAt: 100, Revision: 1,
	}
	if err := db.InsertAdminAPIToken(row); err != nil {
		t.Fatal(err)
	}
	got, found, err := db.GetAdminAPITokenByHash("hash")
	if err != nil || !found || got.TokenHash != "hash" {
		t.Fatalf("token = %#v, found=%v err=%v", got, found, err)
	}
	if err := db.InsertAdminAPITokenLog(store.AdminAPITokenLogRow{
		TokenID: row.ID, TokenPrefix: row.Prefix, Method: "GET", Path: "/v1/admin/channels",
		Scope: "read", Decision: "allow", Status: 200,
	}); err != nil {
		t.Fatal(err)
	}
	logs, err := db.ListAdminAPITokenLogs(10)
	if err != nil || len(logs) != 1 || logs[0].Decision != "allow" {
		t.Fatalf("logs = %#v, err=%v", logs, err)
	}
	if err := db.RotateAdminAPIToken(row.ID, "new-hash", "kiln_v1_new", 1); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := db.GetAdminAPITokenByHash("hash"); found {
		t.Fatal("old token hash survived rotation")
	}
	rotated, found, _ := db.GetAdminAPITokenByHash("new-hash")
	if !found || rotated.Revision != 2 {
		t.Fatalf("rotated = %#v", rotated)
	}
}

func TestAuthOverridesPreserveConfigAuthorization(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	configured := []config.User{{Username: "admin", PasswordHash: "old-hash", Role: "admin", ChannelIDs: []string{"one"}}}
	users, err := db.ApplyAuthOverrides(configured)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Revision != 1 || users[0].ConfigName != "admin" {
		t.Fatalf("initial users = %+v", users)
	}
	updated := users[0]
	updated.Username = "kiln-admin"
	updated.PasswordHash = "new-hash"
	if err := db.ReplaceAuthUser("admin", updated, 1); err != nil {
		t.Fatal(err)
	}

	reconfigured := []config.User{
		{Username: "admin", PasswordHash: "config-hash", Role: "viewer", ChannelIDs: []string{"two"}},
		{Username: "operator", PasswordHash: "operator-hash", Role: "viewer"},
	}
	users, err = db.ApplyAuthOverrides(reconfigured)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 {
		t.Fatalf("users = %+v", users)
	}
	if users[0].Username != "kiln-admin" || users[0].PasswordHash != "new-hash" || users[0].Role != "viewer" ||
		len(users[0].ChannelIDs) != 1 || users[0].ChannelIDs[0] != "two" || users[0].Revision != 2 {
		t.Fatalf("overridden user = %+v", users[0])
	}
	if users[1].Username != "operator" || users[1].Revision != 1 {
		t.Fatalf("new config user = %+v", users[1])
	}
}

func createV1Database(t *testing.T, dir string) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "kiln.db"))
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	defer raw.Close()

	const schema = `
CREATE TABLE schema_version (version INTEGER NOT NULL);
INSERT INTO schema_version(version) VALUES (1);
CREATE TABLE channels (
  id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '', group_name TEXT NOT NULL DEFAULT '',
  logo_url TEXT NOT NULL DEFAULT '', upstream TEXT NOT NULL, path TEXT NOT NULL, ingress TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0, on_demand INTEGER NOT NULL DEFAULT 1,
  autostart INTEGER NOT NULL DEFAULT 0, idle_timeout_sec INTEGER NOT NULL DEFAULT 90,
  max_viewers INTEGER NOT NULL DEFAULT 0, keys_file TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '', headers_json TEXT NOT NULL DEFAULT '{}',
  restart_on_failure INTEGER NOT NULL DEFAULT 0, prefer_height INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0, updated_at INTEGER NOT NULL
);
CREATE TABLE access_tokens (
  id TEXT PRIMARY KEY, name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL, scope_json TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
  note TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0, revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE access_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT, token_id TEXT NOT NULL DEFAULT '',
  token_prefix TEXT NOT NULL DEFAULT '', path TEXT NOT NULL DEFAULT '',
  channel_id TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 0,
  remote TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL
);
CREATE TABLE proxy_profiles (
  id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', url TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE proxy_rules (
  id TEXT PRIMARY KEY, priority INTEGER NOT NULL DEFAULT 100,
  kind TEXT NOT NULL DEFAULT 'host_suffix', pattern TEXT NOT NULL DEFAULT '',
  proxy_id TEXT NOT NULL DEFAULT 'direct', disabled INTEGER NOT NULL DEFAULT 0
);
INSERT INTO channels VALUES
  ('default-agent','Default','News','','origin','/default.m3u8','hls',0,1,0,90,0,'','Kiln/0.2','{}',0,0,0,101),
  ('custom-agent','Custom','News','','origin','/custom.m3u8','hls',0,1,0,90,0,'','Custom/7','{}',0,0,1,102);
INSERT INTO access_tokens VALUES
  ('legacy-token','Legacy','legacy-hash','kiln_old','{"channels":[]}',1,'kept',103,0,0);
INSERT INTO settings VALUES ('public_base_url','https://kiln.example');
INSERT INTO access_logs(token_id, token_prefix, path, channel_id, status, remote, created_at)
  VALUES ('legacy-token','kiln_old','/p/v1plaintextsecret/playlist.m3u','default-agent',200,'127.0.0.1',104);
INSERT INTO proxy_profiles VALUES ('proxy-1','Proxy','http://127.0.0.1:8080',0);
INSERT INTO proxy_rules VALUES ('rule-1',10,'host_suffix','example.com','proxy-1',0);
`
	if _, err := raw.Exec(schema); err != nil {
		t.Fatalf("create v1 fixture: %v", err)
	}
}

func TestChannelRevisionRejectsStaleUpdate(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	channel := config.Channel{ID: "news", Title: "News", Upstream: "origin", Path: "/news.m3u8", Ingress: "hls"}
	if err := db.UpsertChannel(channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	created, ok, err := db.GetChannelRow(channel.ID)
	if err != nil || !ok {
		t.Fatalf("get created channel: found=%v err=%v", ok, err)
	}
	if created.Revision != 1 || created.UpdatedAt == 0 {
		t.Fatalf("created metadata = revision %d, updated_at %d", created.Revision, created.UpdatedAt)
	}

	channel.Title = "Updated"
	if err := db.UpsertChannelIfRevision(channel, created.Revision); err != nil {
		t.Fatalf("update current revision: %v", err)
	}
	updated, _, err := db.GetChannelRow(channel.ID)
	if err != nil {
		t.Fatalf("get updated channel: %v", err)
	}
	if updated.Revision != 2 || updated.Channel.Title != "Updated" {
		t.Fatalf("updated row = %#v", updated)
	}

	channel.Title = "Stale"
	if err := db.UpsertChannelIfRevision(channel, created.Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale update error = %v, want ErrRevisionConflict", err)
	}
	unchanged, _, err := db.GetChannelRow(channel.ID)
	if err != nil || unchanged.Channel.Title != "Updated" || unchanged.Revision != 2 {
		t.Fatalf("row after stale update = %#v, %v", unchanged, err)
	}
	if err := db.DeleteChannelIfRevision(channel.ID, created.Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale delete error = %v, want ErrRevisionConflict", err)
	}
	if err := db.DeleteChannelIfRevision(channel.ID, unchanged.Revision); err != nil {
		t.Fatalf("delete current revision: %v", err)
	}
	if _, ok, err := db.GetChannelRow(channel.ID); err != nil || ok {
		t.Fatalf("channel after delete: found=%v err=%v", ok, err)
	}
}

func TestChannelEPGFieldsRoundTrip(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	want := config.Channel{
		ID: "news", Title: "News", Upstream: "origin", Path: "/news.m3u8", Ingress: "hls",
		EPGID: "368359", EPGName: "無綫新聞台", EPGSource: "hk-1",
		PreferredAudioLanguages: []string{"yue", "zh"},
	}
	if err := db.UpsertChannel(want); err != nil {
		t.Fatalf("upsert channel: %v", err)
	}
	got, ok, err := db.GetChannel(want.ID)
	if err != nil || !ok {
		t.Fatalf("get channel: found=%v err=%v", ok, err)
	}
	if got.EPGID != want.EPGID || got.EPGName != want.EPGName || got.EPGSource != want.EPGSource {
		t.Fatalf("EPG fields = %#v, want %#v", got, want)
	}
	if len(got.PreferredAudioLanguages) != 2 || got.PreferredAudioLanguages[0] != "yue" || got.PreferredAudioLanguages[1] != "zh" {
		t.Fatalf("preferred audio languages = %v", got.PreferredAudioLanguages)
	}
}

func TestChannelSourceURLRoundTrip(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	want := config.Channel{
		ID: "direct", Title: "Direct", SourceURL: "https://media.example/live/index.m3u8?token=abc", Ingress: "hls",
	}
	if err := db.UpsertChannel(want); err != nil {
		t.Fatalf("upsert direct channel: %v", err)
	}
	got, ok, err := db.GetChannel(want.ID)
	if err != nil || !ok {
		t.Fatalf("get direct channel: found=%v err=%v", ok, err)
	}
	if got.SourceURL != want.SourceURL {
		t.Fatalf("source URL = %q, want %q", got.SourceURL, want.SourceURL)
	}

	want.SourceURL = "https://other.example/channel.m3u8"
	row, _, err := db.GetChannelRow(want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertChannelIfRevision(want, row.Revision); err != nil {
		t.Fatalf("update source URL: %v", err)
	}
	got, _, err = db.GetChannel(want.ID)
	if err != nil || got.SourceURL != want.SourceURL {
		t.Fatalf("updated source URL = %q, %v", got.SourceURL, err)
	}
}

func TestSetAllChannelsDisabledReturnsChangedIDs(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	for _, channel := range []config.Channel{
		{ID: "one", Upstream: "origin", Path: "/one", Ingress: "hls"},
		{ID: "two", Upstream: "origin", Path: "/two", Ingress: "hls", Disabled: true},
	} {
		if err := db.UpsertChannel(channel); err != nil {
			t.Fatalf("upsert %s: %v", channel.ID, err)
		}
	}
	changed, err := db.SetAllChannelsDisabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "one" {
		t.Fatalf("disabled changed IDs = %v", changed)
	}
	rows, err := db.ListChannelRows(true)
	if err != nil || !rows[0].Channel.Disabled || !rows[1].Channel.Disabled || rows[0].Revision != 2 || rows[1].Revision != 1 {
		t.Fatalf("rows after disable = %#v, %v", rows, err)
	}
	changed, err = db.SetAllChannelsDisabled(false)
	if err != nil || len(changed) != 2 {
		t.Fatalf("enable changed IDs = %v, %v", changed, err)
	}
}

func TestEPGSourceRevisionRoundTrip(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	want := store.EPGSourceRow{
		ID: "custom", Name: "Custom", URL: "https://epg.example/guide.xml.gz",
		Timezone: "Asia/Shanghai", Proxy: "auto", Enabled: true,
	}
	if err := db.UpsertEPGSource(want); err != nil {
		t.Fatalf("upsert EPG source: %v", err)
	}
	rows, err := db.ListEPGSources()
	if err != nil || len(rows) != 1 {
		t.Fatalf("list EPG sources = %#v, %v", rows, err)
	}
	if rows[0].URL != want.URL || rows[0].Revision != 1 {
		t.Fatalf("EPG source = %#v, want %#v", rows[0], want)
	}
	want.Enabled = false
	if err := db.UpsertEPGSourceIfRevision(want, rows[0].Revision); err != nil {
		t.Fatalf("update EPG source: %v", err)
	}
	if err := db.UpsertEPGSourceIfRevision(want, rows[0].Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale EPG source update error = %v", err)
	}
}

func TestEPGSourceCanPersistADeletionBeforeItHasAnOverride(t *testing.T) {
	directory := t.TempDir()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.HideEPGSourceIfRevision("hk-1", 0); err != nil {
		t.Fatalf("hide pristine preset: %v", err)
	}
	rows, err := db.ListEPGSources()
	if err != nil || len(rows) != 1 {
		t.Fatalf("hidden rows = %#v, %v", rows, err)
	}
	if !rows[0].Deleted || rows[0].Enabled || rows[0].Revision != 1 {
		t.Fatalf("hidden preset = %+v", rows[0])
	}
	if err := db.HideEPGSourceIfRevision("hk-1", 0); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("concurrent pristine delete error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	rows, err = db.ListEPGSources()
	if err != nil || len(rows) != 1 || !rows[0].Deleted {
		t.Fatalf("hidden preset after restart = %#v, %v", rows, err)
	}
}

func TestAccessLogsCanBeDeletedByAgeAndCleared(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	for _, createdAt := range []int64{100, 200, 300} {
		if err := db.InsertAccessLog(store.AccessLogRow{Path: "/redacted", CreatedAt: createdAt}); err != nil {
			t.Fatalf("insert log at %d: %v", createdAt, err)
		}
	}
	deleted, err := db.DeleteAccessLogsBefore(250)
	if err != nil || deleted != 2 {
		t.Fatalf("delete old logs = %d, %v; want 2", deleted, err)
	}
	logs, err := db.ListAccessLogs(10, "")
	if err != nil || len(logs) != 1 || logs[0].CreatedAt != 300 {
		t.Fatalf("logs after age cleanup = %#v, %v", logs, err)
	}
	deleted, err = db.ClearAccessLogs()
	if err != nil || deleted != 1 {
		t.Fatalf("clear logs = %d, %v; want 1", deleted, err)
	}
	logs, err = db.ListAccessLogs(10, "")
	if err != nil || len(logs) != 0 {
		t.Fatalf("logs after clear = %#v, %v", logs, err)
	}
}

func TestEditableResourcesRejectStaleRevisions(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	profile := store.ProxyProfileRow{ID: "proxy", Name: "First", URL: "http://127.0.0.1:8080"}
	if err := db.UpsertProxyProfile(profile); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	profiles, err := db.ListProxyProfiles()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("list profiles = %#v, %v", profiles, err)
	}
	profile.Name = "Second"
	if err := db.UpsertProxyProfileIfRevision(profile, profiles[0].Revision); err != nil {
		t.Fatalf("update current profile: %v", err)
	}
	profile.Name = "Stale"
	if err := db.UpsertProxyProfileIfRevision(profile, profiles[0].Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale profile update error = %v", err)
	}

	rule := store.ProxyRuleRow{ID: "rule", Priority: 10, Kind: "host_suffix", Pattern: "example.com", ProxyID: "proxy"}
	if err := db.UpsertProxyRule(rule); err != nil {
		t.Fatalf("create rule: %v", err)
	}
	rules, err := db.ListProxyRules()
	if err != nil || len(rules) != 1 {
		t.Fatalf("list rules = %#v, %v", rules, err)
	}
	rule.Pattern = "new.example.com"
	if err := db.UpsertProxyRuleIfRevision(rule, rules[0].Revision); err != nil {
		t.Fatalf("update current rule: %v", err)
	}
	rule.Pattern = "stale.example.com"
	if err := db.UpsertProxyRuleIfRevision(rule, rules[0].Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale rule update error = %v", err)
	}

	if err := db.SetSetting("public_base_url", "https://first.example"); err != nil {
		t.Fatalf("create setting: %v", err)
	}
	setting, ok, err := db.GetSettingRow("public_base_url")
	if err != nil || !ok || setting.Revision != 1 || setting.UpdatedAt == 0 {
		t.Fatalf("created setting = %#v, found=%v err=%v", setting, ok, err)
	}
	if err := db.SetSettingIfRevision("public_base_url", "https://second.example", setting.Revision); err != nil {
		t.Fatalf("update current setting: %v", err)
	}
	if err := db.SetSettingIfRevision("public_base_url", "https://stale.example", setting.Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale setting update error = %v", err)
	}

	token := store.AccessTokenRow{ID: "token", Name: "Token", TokenHash: "hash", Prefix: "kiln_123", ScopeJSON: "{}", Enabled: true, CreatedAt: 100}
	if err := db.InsertAccessToken(token); err != nil {
		t.Fatalf("create token: %v", err)
	}
	createdToken, ok, err := db.GetAccessTokenByHash(token.TokenHash)
	if err != nil || !ok || createdToken.Revision != 1 {
		t.Fatalf("created token = %#v, found=%v err=%v", createdToken, ok, err)
	}
	if err := db.RevokeAccessTokenIfRevision(token.ID, createdToken.Revision); err != nil {
		t.Fatalf("revoke current token: %v", err)
	}
	revokedToken, ok, err := db.GetAccessTokenByHash(token.TokenHash)
	if err != nil || !ok || revokedToken.Enabled || revokedToken.RevokedAt == 0 || revokedToken.Revision != 2 {
		t.Fatalf("revoked token = %#v, found=%v err=%v", revokedToken, ok, err)
	}
	if err := db.RevokeAccessTokenIfRevision(token.ID, createdToken.Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale token revoke error = %v", err)
	}
}

func TestConfigurationReplacementsAreAtomicAndRevisionChecked(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := db.SeedFromConfig(config.File{Egress: config.Egress{Default: "direct", PlaylistPolicy: "rewrite"}}); err != nil {
		t.Fatalf("seed egress: %v", err)
	}

	egressRevision, ok, err := db.GetSettingRow("egress_default")
	if err != nil || !ok {
		t.Fatalf("get egress revision: found=%v err=%v", ok, err)
	}
	duplicateProfiles := []store.ProxyProfileRow{
		{ID: "same", URL: "http://127.0.0.1:8080"},
		{ID: "same", URL: "http://127.0.0.1:8081"},
	}
	if err := db.ReplaceEgressConfiguration("same", "rewrite", "host.docker.internal", duplicateProfiles, nil, egressRevision.Revision); err == nil {
		t.Fatal("duplicate profile replacement unexpectedly succeeded")
	}
	defaultValue, _, err := db.GetSetting("egress_default")
	if err != nil || defaultValue != "direct" {
		t.Fatalf("failed replacement partially changed default: %q, %v", defaultValue, err)
	}
	profiles, err := db.ListProxyProfiles()
	if err != nil || len(profiles) != 0 {
		t.Fatalf("failed replacement partially changed profiles: %#v, %v", profiles, err)
	}

	profiles = []store.ProxyProfileRow{{ID: "proxy", URL: "http://127.0.0.1:8080"}}
	if err := db.ReplaceEgressConfiguration("proxy", "auto", "host.docker.internal", profiles, nil, egressRevision.Revision); err != nil {
		t.Fatalf("replace egress: %v", err)
	}
	if err := db.ReplaceEgressConfiguration("direct", "rewrite", "host.docker.internal", nil, nil, egressRevision.Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale egress replacement error = %v", err)
	}

	runtimeRevision, ok, err := db.GetSettingRow("runtime_settings_revision")
	if err != nil || !ok {
		t.Fatalf("get runtime revision: found=%v err=%v", ok, err)
	}
	if err := db.ReplaceRuntimeSettings("https://kiln.example", "30", runtimeRevision.Revision); err != nil {
		t.Fatalf("replace runtime settings: %v", err)
	}
	if err := db.ReplaceRuntimeSettings("https://stale.example", "10", runtimeRevision.Revision); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale runtime replacement error = %v", err)
	}
	publicBase, _, err := db.GetSetting("public_base_url")
	if err != nil || publicBase != "https://kiln.example" {
		t.Fatalf("stale runtime replacement changed public base: %q, %v", publicBase, err)
	}
}
