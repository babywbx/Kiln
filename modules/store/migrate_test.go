package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

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
