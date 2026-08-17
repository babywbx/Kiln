package store

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/channelconfig"
	"github.com/babywbx/kiln/modules/config"
)

func TestOpenMigratesFromEveryIntermediateVersion(t *testing.T) {
	for start := 1; start < currentSchemaVersion; start++ {
		t.Run(fmt.Sprintf("v%d", start), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			seedDatabaseAtVersion(t, dir, start)

			db, err := Open(dir)
			if err != nil {
				t.Fatalf("open v%d database: %v", start, err)
			}
			assertMigratedDatabase(t, db, start)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			db, err = Open(dir)
			if err != nil {
				t.Fatalf("reopen migrated database: %v", err)
			}
			defer db.Close()
			assertMigratedDatabase(t, db, start)
		})
	}
}

func TestV100Schema13UpgradeSmoke(t *testing.T) {
	assertV100MigrationsFrozen(t)
	dir := t.TempDir()
	seedDatabaseAtVersion(t, dir, 13)

	const passwordHash = "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie"
	accessToken := "v1" + strings.Repeat("A", 126)
	adminToken := "kiln_v1_" + strings.Repeat("B", 48)
	accessTokenHash := fixtureTokenHash(accessToken)
	adminTokenHash := fixtureTokenHash(adminToken)
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "kiln.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
UPDATE channels SET title='Upgrade Fixture', headers_json='{"Authorization":"Bearer fixture"}', revision=7
  WHERE id='default-agent';
UPDATE proxy_profiles SET name='Release Proxy', url='http://fixture:secret@proxy.example:8080', revision=3
  WHERE id='proxy-1';
UPDATE proxy_rules SET revision=5 WHERE id='rule-1';
INSERT INTO settings(key, value, revision, updated_at) VALUES
  ('egress_default','proxy-1',4,108),
  ('playlist_policy','rewrite',4,108);
`); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE auth_overrides SET username='release-admin', password_hash=?, revision=6
  WHERE config_username='admin'`, passwordHash); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE access_tokens SET token_hash=?, token_prefix=?, scope_json='["default-agent"]', revision=8, updated_at=109
  WHERE id='legacy-token'`, accessTokenHash, accessToken[:10]); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE admin_api_tokens SET token_hash=?, token_prefix=?, scopes_json='["read","write"]', revision=9, updated_at=110
  WHERE id='legacy-admin-token'`, adminTokenHash, adminToken[:16]); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	var startVersion int
	if err := raw.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&startVersion); err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if startVersion != 13 {
		t.Fatalf("fixture schema version = %d, want 13", startVersion)
	}

	preUpgradeAuth, err := auth.New(config.Auth{Users: []config.User{{
		ConfigName: "admin", Username: "release-admin", PasswordHash: passwordHash, Role: "admin", Revision: 6,
	}}}, time.Hour, auth.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	preUpgradeLogin, err := preUpgradeAuth.Login("release-admin", "admin")
	if err != nil {
		t.Fatalf("v1.0.0 credentials: %v", err)
	}
	keyPath := filepath.Join(dir, "auth", "ed25519.pem")
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("upgrade schema 13 database: %v", err)
	}
	defer db.Close()
	startupCfg := config.File{
		Upstreams: []config.Upstream{{ID: "origin", BaseURL: "https://origin.example"}},
		Proxies:   []config.ProxyProfile{{ID: "proxy-1", Name: "Release Proxy", URL: "http://proxy.example:8080"}},
		Egress: config.Egress{
			Default: "proxy-1", PlaylistPolicy: "rewrite",
			Rules: []config.EgressRule{{ID: "rule-1", Priority: 10, Kind: "host_suffix", Pattern: "example.com", Proxy: "proxy-1"}},
		},
	}
	if err := db.SeedFromConfig(startupCfg); err != nil {
		t.Fatalf("seed current startup config: %v", err)
	}
	var version int
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 14 {
		t.Fatalf("schema version = %d, want 14", version)
	}

	channel, found, err := db.GetChannelRow("default-agent")
	if err != nil || !found {
		t.Fatalf("upgraded channel found=%v err=%v", found, err)
	}
	if channel.Channel.Title != "Upgrade Fixture" || channel.Channel.Headers["Authorization"] != "Bearer fixture" ||
		channel.Channel.Upstream != "origin" || channel.Channel.Path != "/default.m3u8" || channel.Channel.Ingress != "hls" ||
		channel.Channel.Disabled || channel.Channel.UpgradeInsecureRedirects || channel.Revision != 7 {
		t.Fatalf("upgraded channel = %#v", channel)
	}
	sourceURL, err := channelconfig.SourceURL(startupCfg, channel.Channel)
	if err != nil || sourceURL != "https://origin.example/default.m3u8" {
		t.Fatalf("upgraded channel source URL = %q, err=%v", sourceURL, err)
	}
	egress, err := db.GetEgressSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if egress.Default != "proxy-1" || egress.PlaylistPolicy != "rewrite" || egress.Revision != 4 ||
		len(egress.Profiles) != 1 || egress.Profiles[0].URL != "http://fixture:secret@proxy.example:8080" || egress.Profiles[0].Revision != 3 ||
		len(egress.Rules) != 1 || egress.Rules[0].ProxyID != "proxy-1" || egress.Rules[0].Revision != 5 {
		t.Fatalf("upgraded egress = %#v", egress)
	}

	users, err := db.ApplyAuthOverrides([]config.User{{Username: "admin", PasswordHash: passwordHash, Role: "admin"}})
	if err != nil || len(users) != 1 || users[0].Username != "release-admin" || users[0].Revision != 6 {
		t.Fatalf("upgraded auth users = %#v, err=%v", users, err)
	}
	postUpgradeAuth, err := auth.New(config.Auth{Users: users}, time.Hour, auth.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postUpgradeAuth.Parse(preUpgradeLogin.Token); err != nil {
		t.Fatalf("pre-upgrade session token after upgrade: %v", err)
	}
	if _, err := postUpgradeAuth.Login("release-admin", "admin"); err != nil {
		t.Fatalf("upgraded credentials: %v", err)
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil || string(keyAfter) != string(keyBefore) {
		t.Fatalf("signing key changed, err=%v", err)
	}

	if len(accessToken) != 128 || !strings.HasPrefix(accessToken, "v1") {
		t.Fatal("v1.0.0 access token has an invalid format")
	}
	accessRow, found, err := db.GetAccessTokenByHash(accessTokenHash)
	accessScopes := decodeStrings(accessRow.ScopeJSON)
	if err != nil || !found || accessRow.ID != "legacy-token" || accessRow.TokenHash != accessTokenHash || accessRow.Prefix != accessToken[:10] ||
		accessRow.Revision != 8 || !accessRow.Enabled || accessRow.ExpiresAt != 0 || accessRow.RevokedAt != 0 ||
		len(accessScopes) != 1 || accessScopes[0] != "default-agent" {
		t.Fatalf("upgraded access token = %#v, found=%v err=%v", accessRow, found, err)
	}
	if len(adminToken) != len("kiln_v1_")+48 || !strings.HasPrefix(adminToken, "kiln_v1_") {
		t.Fatal("v1.0.0 admin API token has an invalid format")
	}
	adminRow, found, err := db.GetAdminAPITokenByHash(adminTokenHash)
	adminScopes := decodeStrings(adminRow.ScopeJSON)
	if err != nil || !found || adminRow.ID != "legacy-admin-token" || adminRow.TokenHash != adminTokenHash || adminRow.Prefix != adminToken[:16] ||
		adminRow.Revision != 9 || !adminRow.Enabled || adminRow.ExpiresAt != 0 || adminRow.RevokedAt != 0 ||
		len(adminScopes) != 2 || adminScopes[0] != "read" || adminScopes[1] != "write" {
		t.Fatalf("upgraded admin API token = %#v, found=%v err=%v", adminRow, found, err)
	}

	playlist := channelconfig.M3U([]config.Channel{channel.Channel}, channelconfig.M3UOptions{
		PublicBase: "https://kiln.example", PlayPathPrefix: "/p/" + accessToken + "/play/",
	})
	playURL := "https://kiln.example/p/" + accessToken + "/play/default-agent/index.m3u8"
	if !strings.Contains(playlist, playURL) {
		t.Fatalf("upgraded playlist missing play URL: %s", playlist)
	}
}

func fixtureTokenHash(token string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
}

func assertV100MigrationsFrozen(t *testing.T) {
	t.Helper()
	const want = "6efa5b758f4b6cb1fc078fed10dbd7a29ee9e09be17e44ed0c47ce820e489d23"
	got := fmt.Sprintf("%x", sha256.Sum256([]byte(
		schemaV1+schemaV2+schemaV3+schemaV4+schemaV5+schemaV6+schemaV7+
			schemaV8+schemaV9+schemaV10+schemaV11+schemaV12+schemaV13,
	)))
	if got != want {
		t.Fatalf("v1.0.0 migration snapshot hash = %s, want %s", got, want)
	}
}

func seedDatabaseAtVersion(t *testing.T, dir string, target int) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "kiln.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := raw.Close(); err != nil {
			t.Fatal(err)
		}
	}()
	tx, err := raw.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= target; version++ {
		if err := applyMigration(tx, version); err != nil {
			t.Fatalf("build fixture at v%d: %v", version, err)
		}
		if err := seedEraData(tx, version); err != nil {
			t.Fatalf("seed v%d era data: %v", version, err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, target); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func seedEraData(tx *sql.Tx, version int) error {
	var err error
	switch version {
	case 1:
		_, err = tx.Exec(`
INSERT INTO channels(id, title, group_name, upstream, path, ingress, keys_file, user_agent, sort_order, updated_at) VALUES
  ('default-agent','Default','News','origin','/default.m3u8','hls','/old/channel.keys','Kiln/0.2',0,101),
  ('custom-agent','Custom','News','origin','/custom.m3u8','hls','','Custom/7',1,102);
INSERT INTO access_tokens(id, name, token_hash, token_prefix, scope_json, enabled, note, created_at) VALUES
  ('legacy-token','Legacy','legacy-hash','kiln_old','{"channels":[]}',1,'kept',103);
INSERT INTO settings(key, value) VALUES ('public_base_url','https://kiln.example');
INSERT INTO access_logs(token_id, token_prefix, path, channel_id, status, remote, created_at) VALUES
  ('legacy-token','kiln_old','/p/v1plaintextsecret/playlist.m3u','default-agent',200,'127.0.0.1',104);
INSERT INTO proxy_profiles(id, name, url) VALUES ('proxy-1','Proxy','http://127.0.0.1:8080');
INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id) VALUES ('rule-1',10,'host_suffix','example.com','proxy-1');
`)
	case 6:
		_, err = tx.Exec(`UPDATE channels
			SET keys='00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100'
			WHERE id='default-agent'`)
	case 7:
		_, err = tx.Exec(`INSERT INTO epg_sources(id, name, url, timezone, proxy, enabled, updated_at)
			VALUES ('legacy-epg','Legacy EPG','https://epg.example/guide.xml','Asia/Shanghai','direct',1,105)`)
	case 9:
		_, err = tx.Exec(`INSERT INTO auth_overrides(config_username, username, password_hash, revision, updated_at)
			VALUES ('admin','kiln-admin','override-hash',2,106)`)
	case 11:
		_, err = tx.Exec(`INSERT INTO admin_api_tokens(id, name, token_hash, token_prefix, scopes_json, enabled, created_at)
			VALUES ('legacy-admin-token','Automation','admin-hash','kiln_v1_legacy','["read"]',1,107)`)
	}
	return err
}

func assertMigratedDatabase(t *testing.T, db *DB, start int) {
	t.Helper()

	var version int
	if err := db.sql.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}

	channels, err := db.ListChannels(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 2 || channels[0].ID != "default-agent" || channels[1].ID != "custom-agent" {
		t.Fatalf("channels = %#v", channels)
	}
	if channels[0].UserAgent != "" || channels[1].UserAgent != "Custom/7" {
		t.Fatalf("user agents = %q, %q", channels[0].UserAgent, channels[1].UserAgent)
	}
	var keysFile, keys string
	if err := db.sql.QueryRow(`SELECT keys_file, keys FROM channels WHERE id='default-agent'`).Scan(&keysFile, &keys); err != nil {
		t.Fatal(err)
	}
	if keysFile != "" || keys != "" {
		t.Fatalf("channel keys not scrubbed: keys_file=%q keys=%q", keysFile, keys)
	}

	tokens, err := db.ListAccessTokens()
	if err != nil || len(tokens) != 1 || tokens[0].ID != "legacy-token" || !tokens[0].Enabled || tokens[0].ExpiresAt != 0 {
		t.Fatalf("tokens = %#v, err=%v", tokens, err)
	}

	logs, err := db.ListAccessLogs(10, "")
	if err != nil || len(logs) != 1 || logs[0].Path != "/p/kiln_old…/playlist.m3u" {
		t.Fatalf("logs = %#v, err=%v", logs, err)
	}

	if value, ok, err := db.GetSetting("public_base_url"); err != nil || !ok || value != "https://kiln.example" {
		t.Fatalf("public_base_url = %q, found=%v err=%v", value, ok, err)
	}
	if value, ok, err := db.GetSetting("runtime_settings_revision"); err != nil || !ok || value != "1" {
		t.Fatalf("runtime_settings_revision = %q, found=%v err=%v", value, ok, err)
	}

	profiles, err := db.ListProxyProfiles()
	if err != nil || len(profiles) != 1 || profiles[0].ID != "proxy-1" {
		t.Fatalf("profiles = %#v, err=%v", profiles, err)
	}
	rules, err := db.ListProxyRules()
	if err != nil || len(rules) != 1 || rules[0].ID != "rule-1" || rules[0].ProxyID != "proxy-1" {
		t.Fatalf("rules = %#v, err=%v", rules, err)
	}

	sources, err := db.ListEPGSources()
	if err != nil {
		t.Fatal(err)
	}
	if start >= 7 {
		if len(sources) != 1 || sources[0].ID != "legacy-epg" || !sources[0].Enabled || sources[0].Deleted {
			t.Fatalf("epg sources = %#v", sources)
		}
	} else if len(sources) != 0 {
		t.Fatalf("epg sources = %#v, want none", sources)
	}

	users, err := db.ApplyAuthOverrides([]config.User{{Username: "admin", PasswordHash: "config-hash", Role: "admin"}})
	if err != nil || len(users) != 1 {
		t.Fatalf("auth users = %#v, err=%v", users, err)
	}
	if start >= 9 {
		if users[0].Username != "kiln-admin" || users[0].PasswordHash != "override-hash" || users[0].Revision != 2 {
			t.Fatalf("overridden user = %+v", users[0])
		}
	} else if users[0].Username != "admin" || users[0].Revision != 1 {
		t.Fatalf("config user = %+v", users[0])
	}

	adminToken, found, err := db.GetAdminAPITokenByHash("admin-hash")
	if err != nil {
		t.Fatal(err)
	}
	if start >= 11 {
		if !found || adminToken.ID != "legacy-admin-token" || !adminToken.Enabled || adminToken.Revision != 1 {
			t.Fatalf("admin token = %#v, found=%v", adminToken, found)
		}
	} else if found {
		t.Fatalf("unexpected admin token = %#v", adminToken)
	}
}
