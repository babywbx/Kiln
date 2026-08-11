package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/config"
	_ "modernc.org/sqlite"
)

type DB struct {
	sql                *sql.DB
	mu                 sync.Mutex
	accessTokens       map[string]AccessTokenRow
	accessTokenTouches map[string]time.Time
}

var (
	ErrRevisionConflict = errors.New("store revision conflict")
	ErrUsernameConflict = errors.New("store username conflict")
)

const currentSchemaVersion = 14

const accessTokenTouchInterval = time.Minute

func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "kiln.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := restrictSQLiteSidecarPermissions(path); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	db := &DB{sql: sqlDB}
	if err := db.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := restrictSQLiteSidecarPermissions(path); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

func restrictSQLiteSidecarPermissions(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

func (db *DB) migrate() error {
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		return err
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	for version < currentSchemaVersion {
		next := version + 1
		if err := applyMigration(tx, next); err != nil {
			return fmt.Errorf("migrate database to version %d: %w", next, err)
		}
		if _, err := tx.Exec(`DELETE FROM schema_version`); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO schema_version(version) VALUES (?)`, next); err != nil {
			return err
		}
		version = next
	}
	return tx.Commit()
}

func applyMigration(tx *sql.Tx, version int) error {
	switch version {
	case 1:
		_, err := tx.Exec(schemaV1)
		return err
	case 2:
		_, err := tx.Exec(schemaV2)
		return err
	case 3:
		_, err := tx.Exec(schemaV3)
		return err
	case 4:
		_, err := tx.Exec(schemaV4)
		return err
	case 5:
		_, err := tx.Exec(schemaV5)
		return err
	case 6:
		_, err := tx.Exec(schemaV6)
		return err
	case 7:
		_, err := tx.Exec(schemaV7)
		return err
	case 8:
		_, err := tx.Exec(schemaV8)
		return err
	case 9:
		_, err := tx.Exec(schemaV9)
		return err
	case 10:
		_, err := tx.Exec(schemaV10)
		return err
	case 11:
		_, err := tx.Exec(schemaV11)
		return err
	case 12:
		_, err := tx.Exec(schemaV12)
		return err
	case 13:
		_, err := tx.Exec(schemaV13)
		return err
	case 14:
		_, err := tx.Exec(schemaV14)
		return err
	default:
		return fmt.Errorf("unknown schema version %d", version)
	}
}

const schemaV1 = `
CREATE TABLE channels (
  id TEXT PRIMARY KEY,
  title TEXT NOT NULL DEFAULT '',
  group_name TEXT NOT NULL DEFAULT '',
  logo_url TEXT NOT NULL DEFAULT '',
  upstream TEXT NOT NULL,
  path TEXT NOT NULL,
  ingress TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0,
  on_demand INTEGER NOT NULL DEFAULT 1,
  autostart INTEGER NOT NULL DEFAULT 0,
  idle_timeout_sec INTEGER NOT NULL DEFAULT 90,
  max_viewers INTEGER NOT NULL DEFAULT 0,
  keys_file TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  headers_json TEXT NOT NULL DEFAULT '{}',
  restart_on_failure INTEGER NOT NULL DEFAULT 0,
  prefer_height INTEGER NOT NULL DEFAULT 0,
  sort_order INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE TABLE access_tokens (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL,
  scope_json TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_access_tokens_hash ON access_tokens(token_hash);
CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE access_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id TEXT NOT NULL DEFAULT '',
  token_prefix TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  channel_id TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 0,
  remote TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX idx_access_logs_created ON access_logs(created_at DESC);
CREATE INDEX idx_access_logs_token ON access_logs(token_id, created_at DESC);
CREATE TABLE proxy_profiles (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE proxy_rules (
  id TEXT PRIMARY KEY,
  priority INTEGER NOT NULL DEFAULT 100,
  kind TEXT NOT NULL DEFAULT 'host_suffix',
  pattern TEXT NOT NULL DEFAULT '',
  proxy_id TEXT NOT NULL DEFAULT 'direct',
  disabled INTEGER NOT NULL DEFAULT 0
);
`

const schemaV2 = `
ALTER TABLE channels ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE access_tokens ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE access_tokens ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE access_tokens ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
UPDATE access_tokens SET updated_at = created_at WHERE updated_at = 0;
ALTER TABLE settings ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE settings ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxy_profiles ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE proxy_profiles ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE proxy_rules ADD COLUMN revision INTEGER NOT NULL DEFAULT 1;
ALTER TABLE proxy_rules ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
UPDATE settings SET updated_at = unixepoch() WHERE updated_at = 0;
UPDATE proxy_profiles SET updated_at = unixepoch() WHERE updated_at = 0;
UPDATE proxy_rules SET updated_at = unixepoch() WHERE updated_at = 0;
UPDATE channels SET user_agent = '' WHERE user_agent = 'Kiln/0.2';
`

const schemaV3 = `
UPDATE access_logs
SET path = '/p/' || CASE WHEN token_prefix = '' THEN '[redacted]' ELSE token_prefix END || '…/' ||
  substr(substr(path, 4), instr(substr(path, 4), '/') + 1)
WHERE path LIKE '/p/%' AND instr(substr(path, 4), '/') > 0;
`

const schemaV4 = `
INSERT OR IGNORE INTO settings(key, value, revision, updated_at)
VALUES ('runtime_settings_revision', '1', 1, unixepoch());
`

const schemaV5 = `
ALTER TABLE channels ADD COLUMN packager TEXT NOT NULL DEFAULT '';
`

const schemaV6 = `
ALTER TABLE channels ADD COLUMN keys TEXT NOT NULL DEFAULT '';
`

const schemaV7 = `
ALTER TABLE channels ADD COLUMN epg_id TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN epg_name TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN epg_source TEXT NOT NULL DEFAULT '';
CREATE TABLE epg_sources (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  timezone TEXT NOT NULL DEFAULT '',
  proxy TEXT NOT NULL DEFAULT 'direct',
  enabled INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL DEFAULT 0
);
`

const schemaV8 = `
ALTER TABLE channels ADD COLUMN preferred_audio_languages_json TEXT NOT NULL DEFAULT '[]';
`

const schemaV9 = `
CREATE TABLE auth_overrides (
  config_username TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 2,
  updated_at INTEGER NOT NULL DEFAULT 0
);
ALTER TABLE channels ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
`

const schemaV10 = `
ALTER TABLE channels ADD COLUMN selection_json TEXT NOT NULL DEFAULT '{}';
`

const schemaV11 = `
CREATE TABLE admin_api_tokens (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  token_prefix TEXT NOT NULL,
  scopes_json TEXT NOT NULL,
  enabled INTEGER NOT NULL DEFAULT 1,
  note TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL DEFAULT 0,
  last_used_at INTEGER NOT NULL DEFAULT 0,
  revoked_at INTEGER NOT NULL DEFAULT 0,
  revision INTEGER NOT NULL DEFAULT 1,
  updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_admin_api_tokens_hash ON admin_api_tokens(token_hash);
CREATE TABLE admin_api_token_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id TEXT,
  token_prefix TEXT NOT NULL DEFAULT '',
  method TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  required_scope TEXT NOT NULL DEFAULT '',
  decision TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 0,
  remote TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  FOREIGN KEY(token_id) REFERENCES admin_api_tokens(id) ON DELETE SET NULL
);
CREATE INDEX idx_admin_api_token_logs_created ON admin_api_token_logs(created_at DESC);
CREATE INDEX idx_admin_api_token_logs_token ON admin_api_token_logs(token_id, created_at DESC);
`

const schemaV12 = `
ALTER TABLE epg_sources ADD COLUMN deleted INTEGER NOT NULL DEFAULT 0;
`

const schemaV13 = `
UPDATE channels SET keys_file = '', keys = '';
`

const schemaV14 = `
ALTER TABLE channels ADD COLUMN upgrade_insecure_redirects INTEGER NOT NULL DEFAULT 0;
`

func (db *DB) SeedFromConfig(cfg config.File) error {
	if err := db.seedChannels(cfg); err != nil {
		return err
	}
	if err := db.seedEgress(cfg); err != nil {
		return err
	}
	return db.seedEPGSources(cfg)
}

func (db *DB) ApplyAuthOverrides(configured []config.User) ([]config.User, error) {
	rows, err := db.sql.Query(`SELECT config_username, username, password_hash, revision FROM auth_overrides`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	overrides := make(map[string]config.User)
	for rows.Next() {
		var user config.User
		if err := rows.Scan(&user.ConfigName, &user.Username, &user.PasswordHash, &user.Revision); err != nil {
			return nil, err
		}
		overrides[user.ConfigName] = user
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	users := make([]config.User, 0, len(configured))
	seenUsernames := make(map[string]struct{}, len(configured))
	for _, user := range configured {
		user.ConfigName = user.Username
		user.Revision = 1
		if override, ok := overrides[user.ConfigName]; ok {
			user.Username = override.Username
			user.PasswordHash = override.PasswordHash
			user.Revision = override.Revision
		}
		if _, duplicate := seenUsernames[user.Username]; duplicate {
			return nil, fmt.Errorf("duplicate effective auth username %q", user.Username)
		}
		seenUsernames[user.Username] = struct{}{}
		users = append(users, user)
	}
	return users, nil
}

func (db *DB) ReplaceAuthUser(oldUsername string, user config.User, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	configName := user.ConfigName
	if configName == "" {
		configName = oldUsername
	}
	var existingRevision int64
	err = tx.QueryRow(`SELECT revision FROM auth_overrides WHERE config_username=?`, configName).Scan(&existingRevision)
	switch {
	case errors.Is(err, sql.ErrNoRows) && expectedRevision == 1:
		_, err = tx.Exec(`INSERT INTO auth_overrides(config_username, username, password_hash, revision, updated_at)
			VALUES (?,?,?,?,?)`, configName, user.Username, user.PasswordHash, 2, time.Now().Unix())
	case err != nil:
		return err
	case existingRevision != expectedRevision:
		return ErrRevisionConflict
	default:
		result, updateErr := tx.Exec(`UPDATE auth_overrides SET username=?, password_hash=?, revision=revision+1,
			updated_at=? WHERE config_username=? AND revision=?`, user.Username, user.PasswordHash,
			time.Now().Unix(), configName, expectedRevision)
		if updateErr != nil {
			return updateErr
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return ErrRevisionConflict
		}
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ErrUsernameConflict
		}
		return err
	}
	return tx.Commit()
}

func (db *DB) seedEPGSources(cfg config.File) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	var count int
	if err := db.sql.QueryRow(`SELECT COUNT(1) FROM epg_sources`).Scan(&count); err != nil {
		return err
	}
	if count > 0 || len(cfg.EPG.Sources) == 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, source := range cfg.EPG.Sources {
		if _, err := tx.Exec(`INSERT INTO epg_sources(id, name, url, timezone, proxy, enabled, updated_at)
			VALUES (?,?,?,?,?,?,?)`, source.ID, source.Name, source.URL, source.Timezone, source.Proxy, boolInt(source.Enabled), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) seedChannels(cfg config.File) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(1) FROM channels`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	now := time.Now().Unix()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for i, ch := range cfg.Channels {
		if err := insertChannelTx(tx, ch, i, now); err != nil {
			return err
		}
	}
	if cfg.Server.PublicBaseURL != "" {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES ('public_base_url', ?, ?)`, cfg.Server.PublicBaseURL, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) seedEgress(cfg config.File) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(1) FROM proxy_profiles`).Scan(&n); err != nil {
		return err
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	if n == 0 {
		for _, p := range cfg.Proxies {
			if _, err := tx.Exec(`INSERT INTO proxy_profiles(id, name, url, disabled, updated_at) VALUES (?,?,?,?,?)`,
				p.ID, p.Name, p.URL, boolInt(p.Disabled), now); err != nil {
				return err
			}
		}
		for i, r := range cfg.Egress.Rules {
			id := r.ID
			if id == "" {
				id = fmt.Sprintf("rule-%d", i+1)
			}
			if _, err := tx.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled, updated_at) VALUES (?,?,?,?,?,?,?)`,
				id, r.Priority, r.Kind, r.Pattern, r.Proxy, boolInt(r.Disabled), now); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES ('egress_default', ?, ?)`, cfg.Egress.Default, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES ('playlist_policy', ?, ?)`, cfg.Egress.PlaylistPolicy, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES ('docker_proxy_host', ?, ?)`, cfg.Egress.DockerProxyHost, now); err != nil {
		return err
	}
	return tx.Commit()
}

type ProxyProfileRow struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url"`
	Disabled  bool   `json:"disabled"`
	Revision  int64  `json:"revision"`
	UpdatedAt int64  `json:"updated_at"`
}

type ProxyRuleRow struct {
	ID        string `json:"id"`
	Priority  int    `json:"priority"`
	Kind      string `json:"kind"`
	Pattern   string `json:"pattern"`
	ProxyID   string `json:"proxy"`
	Disabled  bool   `json:"disabled"`
	Revision  int64  `json:"revision"`
	UpdatedAt int64  `json:"updated_at"`
}

type EgressSnapshot struct {
	Default         string
	PlaylistPolicy  string
	DockerProxyHost string
	Profiles        []ProxyProfileRow
	Rules           []ProxyRuleRow
	Revision        int64
}

type ChannelEgressBinding struct {
	Mode       string
	ProfileID  string
	NewProfile *ProxyProfileRow
}

const managedChannelRulePrefix = "kiln-channel:"

func ManagedChannelRuleID(channelID string) string {
	return managedChannelRulePrefix + channelID
}

func (db *DB) UpsertChannelWithEgress(ch config.Channel, expectedRevision int64, binding ChannelEgressBinding) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertChannelTx(tx, ch, expectedRevision); err != nil {
		return err
	}
	mode := strings.ToLower(strings.TrimSpace(binding.Mode))
	if mode != "auto" && mode != "direct" && mode != "profile" {
		return fmt.Errorf("channel egress mode invalid")
	}
	profileID := strings.TrimSpace(binding.ProfileID)
	if binding.NewProfile != nil {
		profile := *binding.NewProfile
		if mode != "profile" || profile.ID == "" || profile.URL == "" || profile.Disabled {
			return fmt.Errorf("new channel proxy invalid")
		}
		if _, err := tx.Exec(`INSERT INTO proxy_profiles(id, name, url, disabled, updated_at)
			VALUES (?,?,?,?,?)`, profile.ID, profile.Name, profile.URL, 0, time.Now().Unix()); err != nil {
			return err
		}
		profileID = profile.ID
	}
	if mode == "profile" {
		if profileID == "" {
			return fmt.Errorf("channel proxy required")
		}
		var disabled int
		if err := tx.QueryRow(`SELECT disabled FROM proxy_profiles WHERE id=?`, profileID).Scan(&disabled); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("channel proxy %q not found", profileID)
			}
			return err
		}
		if intBool(disabled) {
			return fmt.Errorf("channel proxy %q is disabled", profileID)
		}
	}
	ruleID := ManagedChannelRuleID(ch.ID)
	if _, err := tx.Exec(`DELETE FROM proxy_rules WHERE id=?`, ruleID); err != nil {
		return err
	}
	if mode != "auto" {
		if mode == "direct" {
			profileID = "direct"
		}
		if _, err := tx.Exec(`INSERT INTO proxy_rules(
			id, priority, kind, pattern, proxy_id, disabled, updated_at
		) VALUES (?, -10000, 'channel_id', ?, ?, 0, ?)`, ruleID, ch.ID, profileID, time.Now().Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE settings SET revision=revision+1, updated_at=? WHERE key='egress_default'`, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) DeleteChannelWithEgress(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var result sql.Result
	if expectedRevision > 0 {
		result, err = tx.Exec(`DELETE FROM channels WHERE id=? AND revision=?`, id, expectedRevision)
	} else {
		result, err = tx.Exec(`DELETE FROM channels WHERE id=?`, id)
	}
	if err != nil {
		return err
	}
	if err := revisionResult(result); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM proxy_rules WHERE id=?`, ManagedChannelRuleID(id)); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE settings SET revision=revision+1, updated_at=? WHERE key='egress_default'`, time.Now().Unix()); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertChannelTx(tx *sql.Tx, ch config.Channel, expectedRevision int64) error {
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := tx.Exec(`UPDATE channels SET
			title=?, group_name=?, logo_url=?, epg_id=?, epg_name=?, epg_source=?, upstream=?, path=?, source_url=?, ingress=?, disabled=?, on_demand=?, autostart=?,
			idle_timeout_sec=?, max_viewers=?, user_agent=?, headers_json=?, restart_on_failure=?,
			prefer_height=?, packager=?, preferred_audio_languages_json=?, selection_json=?, upgrade_insecure_redirects=?, revision=revision+1, updated_at=?
			WHERE id=? AND revision=?`,
			ch.Title, ch.Group, ch.LogoURL, ch.EPGID, ch.EPGName, ch.EPGSource, ch.Upstream, ch.Path, ch.SourceURL, ch.Ingress,
			boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart), ch.IdleTimeoutSec, ch.MaxViewers,
			ch.UserAgent, encodeHeaders(ch.Headers), boolInt(ch.RestartOnFailure), ch.PreferHeight,
			ch.Packager, encodeStrings(ch.PreferredAudioLanguages), encodeSelection(ch.Selection), boolInt(ch.UpgradeInsecureRedirects), now, ch.ID, expectedRevision,
		)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	var maxSort int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM channels`).Scan(&maxSort); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO channels(
		id, title, group_name, logo_url, epg_id, epg_name, epg_source, upstream, path, source_url, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, user_agent, headers_json, restart_on_failure, prefer_height, packager,
		preferred_audio_languages_json, selection_json, upgrade_insecure_redirects, sort_order, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET
		title=excluded.title, group_name=excluded.group_name, logo_url=excluded.logo_url,
		epg_id=excluded.epg_id, epg_name=excluded.epg_name, epg_source=excluded.epg_source,
		upstream=excluded.upstream, path=excluded.path, source_url=excluded.source_url, ingress=excluded.ingress,
		disabled=excluded.disabled, on_demand=excluded.on_demand, autostart=excluded.autostart,
		idle_timeout_sec=excluded.idle_timeout_sec, max_viewers=excluded.max_viewers,
		user_agent=excluded.user_agent, headers_json=excluded.headers_json,
		restart_on_failure=excluded.restart_on_failure, prefer_height=excluded.prefer_height,
		packager=excluded.packager, preferred_audio_languages_json=excluded.preferred_audio_languages_json,
		selection_json=excluded.selection_json, upgrade_insecure_redirects=excluded.upgrade_insecure_redirects,
		revision=channels.revision+1, updated_at=excluded.updated_at`,
		ch.ID, ch.Title, ch.Group, ch.LogoURL, ch.EPGID, ch.EPGName, ch.EPGSource, ch.Upstream, ch.Path, ch.SourceURL, ch.Ingress,
		boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart), ch.IdleTimeoutSec, ch.MaxViewers,
		ch.UserAgent, encodeHeaders(ch.Headers), boolInt(ch.RestartOnFailure), ch.PreferHeight,
		ch.Packager, encodeStrings(ch.PreferredAudioLanguages), encodeSelection(ch.Selection), boolInt(ch.UpgradeInsecureRedirects), maxSort+1, now,
	)
	return err
}

func (db *DB) GetEgressSnapshot() (EgressSnapshot, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	var snapshot EgressSnapshot
	rows, err := db.sql.Query(`SELECT id, name, url, disabled, revision, updated_at FROM proxy_profiles ORDER BY id`)
	if err != nil {
		return EgressSnapshot{}, err
	}
	for rows.Next() {
		var profile ProxyProfileRow
		var disabled int
		if err := rows.Scan(&profile.ID, &profile.Name, &profile.URL, &disabled, &profile.Revision, &profile.UpdatedAt); err != nil {
			_ = rows.Close()
			return EgressSnapshot{}, err
		}
		profile.Disabled = intBool(disabled)
		snapshot.Profiles = append(snapshot.Profiles, profile)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return EgressSnapshot{}, err
	}
	ruleRows, err := db.sql.Query(`SELECT id, priority, kind, pattern, proxy_id, disabled, revision, updated_at FROM proxy_rules ORDER BY priority ASC, id ASC`)
	if err != nil {
		return EgressSnapshot{}, err
	}
	for ruleRows.Next() {
		var rule ProxyRuleRow
		var disabled int
		if err := ruleRows.Scan(&rule.ID, &rule.Priority, &rule.Kind, &rule.Pattern, &rule.ProxyID, &disabled, &rule.Revision, &rule.UpdatedAt); err != nil {
			_ = ruleRows.Close()
			return EgressSnapshot{}, err
		}
		rule.Disabled = intBool(disabled)
		snapshot.Rules = append(snapshot.Rules, rule)
	}
	if err := errors.Join(ruleRows.Err(), ruleRows.Close()); err != nil {
		return EgressSnapshot{}, err
	}
	settingRows, err := db.sql.Query(`SELECT key, value, revision FROM settings
		WHERE key IN ('egress_default','playlist_policy','docker_proxy_host')`)
	if err != nil {
		return EgressSnapshot{}, err
	}
	defer settingRows.Close()
	for settingRows.Next() {
		var key, value string
		var revision int64
		if err := settingRows.Scan(&key, &value, &revision); err != nil {
			return EgressSnapshot{}, err
		}
		switch key {
		case "egress_default":
			snapshot.Default = value
			snapshot.Revision = revision
		case "playlist_policy":
			snapshot.PlaylistPolicy = value
		case "docker_proxy_host":
			snapshot.DockerProxyHost = value
		}
	}
	return snapshot, settingRows.Err()
}

type RuntimeSettingsSnapshot struct {
	Values   map[string]string
	Revision int64
}

func (db *DB) GetRuntimeSettingsSnapshot() (RuntimeSettingsSnapshot, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	snapshot := RuntimeSettingsSnapshot{Values: map[string]string{}}
	rows, err := db.sql.Query(`SELECT key, value, revision FROM settings
		WHERE key IN ('public_base_url','access_log_retention_days','runtime_settings_revision')`)
	if err != nil {
		return RuntimeSettingsSnapshot{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		var revision int64
		if err := rows.Scan(&key, &value, &revision); err != nil {
			return RuntimeSettingsSnapshot{}, err
		}
		if key == "runtime_settings_revision" {
			snapshot.Revision = revision
		} else {
			snapshot.Values[key] = value
		}
	}
	return snapshot, rows.Err()
}

func (db *DB) ListProxyProfiles() ([]ProxyProfileRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, name, url, disabled, revision, updated_at FROM proxy_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyProfileRow
	for rows.Next() {
		var r ProxyProfileRow
		var dis int
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &dis, &r.Revision, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Disabled = intBool(dis)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) UpsertProxyProfile(p ProxyProfileRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertProxyProfile(p, 0)
}

func (db *DB) UpsertProxyProfileIfRevision(p ProxyProfileRow, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertProxyProfile(p, expectedRevision)
}

func (db *DB) upsertProxyProfile(p ProxyProfileRow, expectedRevision int64) error {
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE proxy_profiles SET name=?, url=?, disabled=?, revision=revision+1, updated_at=?
			WHERE id=? AND revision=?`, p.Name, p.URL, boolInt(p.Disabled), now, p.ID, expectedRevision)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	_, err := db.sql.Exec(`INSERT INTO proxy_profiles(id, name, url, disabled, updated_at) VALUES (?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, url=excluded.url, disabled=excluded.disabled,
		revision=proxy_profiles.revision+1, updated_at=excluded.updated_at`,
		p.ID, p.Name, p.URL, boolInt(p.Disabled), now)
	return err
}

func (db *DB) DeleteProxyProfile(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`DELETE FROM proxy_profiles WHERE id = ?`, id)
	return err
}

func (db *DB) ListProxyRules() ([]ProxyRuleRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, priority, kind, pattern, proxy_id, disabled, revision, updated_at FROM proxy_rules ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyRuleRow
	for rows.Next() {
		var r ProxyRuleRow
		var dis int
		if err := rows.Scan(&r.ID, &r.Priority, &r.Kind, &r.Pattern, &r.ProxyID, &dis, &r.Revision, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Disabled = intBool(dis)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) UpsertProxyRule(r ProxyRuleRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertProxyRule(r, 0)
}

func (db *DB) UpsertProxyRuleIfRevision(r ProxyRuleRow, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertProxyRule(r, expectedRevision)
}

func (db *DB) upsertProxyRule(r ProxyRuleRow, expectedRevision int64) error {
	if r.ID == "" {
		return fmt.Errorf("rule id required")
	}
	if r.Kind == "" {
		r.Kind = "host_suffix"
	}
	if r.ProxyID == "" {
		r.ProxyID = "direct"
	}
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE proxy_rules SET priority=?, kind=?, pattern=?, proxy_id=?, disabled=?,
			revision=revision+1, updated_at=? WHERE id=? AND revision=?`,
			r.Priority, r.Kind, r.Pattern, r.ProxyID, boolInt(r.Disabled), now, r.ID, expectedRevision)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	_, err := db.sql.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled, updated_at) VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET priority=excluded.priority, kind=excluded.kind, pattern=excluded.pattern,
		proxy_id=excluded.proxy_id, disabled=excluded.disabled, revision=proxy_rules.revision+1,
		updated_at=excluded.updated_at`,
		r.ID, r.Priority, r.Kind, r.Pattern, r.ProxyID, boolInt(r.Disabled), now)
	return err
}

func (db *DB) DeleteProxyRule(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`DELETE FROM proxy_rules WHERE id = ?`, id)
	return err
}

func (db *DB) ReplaceAllProxyRules(rules []ProxyRuleRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM proxy_rules`); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, r := range rules {
		if r.ID == "" {
			continue
		}
		if r.Kind == "" {
			r.Kind = "host_suffix"
		}
		if r.ProxyID == "" {
			r.ProxyID = "direct"
		}
		if _, err := tx.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled, updated_at) VALUES (?,?,?,?,?,?,?)`,
			r.ID, r.Priority, r.Kind, r.Pattern, r.ProxyID, boolInt(r.Disabled), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ReplaceEgressConfiguration(defaultID, playlistPolicy, dockerProxyHost string, profiles []ProxyProfileRow, rules []ProxyRuleRow, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE settings SET value=?, revision=revision+1, updated_at=?
		WHERE key='egress_default' AND revision=?`, defaultID, now, expectedRevision)
	if err != nil {
		return err
	}
	if err := revisionResult(result); err != nil {
		return err
	}
	for key, value := range map[string]string{
		"playlist_policy":   playlistPolicy,
		"docker_proxy_host": dockerProxyHost,
	} {
		if _, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, revision=settings.revision+1,
			updated_at=excluded.updated_at`, key, value, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM proxy_rules`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM proxy_profiles`); err != nil {
		return err
	}
	for _, profile := range profiles {
		if _, err := tx.Exec(`INSERT INTO proxy_profiles(id, name, url, disabled, updated_at) VALUES (?,?,?,?,?)`,
			profile.ID, profile.Name, profile.URL, boolInt(profile.Disabled), now); err != nil {
			return err
		}
	}
	for _, rule := range rules {
		if _, err := tx.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled, updated_at) VALUES (?,?,?,?,?,?,?)`,
			rule.ID, rule.Priority, rule.Kind, rule.Pattern, rule.ProxyID, boolInt(rule.Disabled), now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertChannelTx(tx *sql.Tx, ch config.Channel, sort int, now int64) error {
	hj, _ := json.Marshal(ch.Headers)
	if hj == nil {
		hj = []byte("{}")
	}
	_, err := tx.Exec(`INSERT INTO channels(
		id, title, group_name, logo_url, epg_id, epg_name, epg_source, upstream, path, source_url, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, user_agent, headers_json, restart_on_failure, prefer_height, packager,
		preferred_audio_languages_json, selection_json, upgrade_insecure_redirects, sort_order, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ch.ID, ch.Title, ch.Group, ch.LogoURL, ch.EPGID, ch.EPGName, ch.EPGSource, ch.Upstream, ch.Path, ch.SourceURL, ch.Ingress,
		boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart),
		ch.IdleTimeoutSec, ch.MaxViewers, ch.UserAgent, string(hj),
		boolInt(ch.RestartOnFailure), ch.PreferHeight, ch.Packager, encodeStrings(ch.PreferredAudioLanguages), encodeSelection(ch.Selection),
		boolInt(ch.UpgradeInsecureRedirects), sort, now,
	)
	return err
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intBool(v int) bool { return v != 0 }

func nonzeroTime(value, fallback int64) int64 {
	if value != 0 {
		return value
	}
	return fallback
}

func decodeHeaders(s string) map[string]string {
	out := map[string]string{}
	if strings.TrimSpace(s) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		return map[string]string{}
	}
	return out
}

func encodeHeaders(h map[string]string) string {
	if h == nil {
		return "{}"
	}
	b, err := json.Marshal(h)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func decodeStrings(s string) []string {
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func encodeStrings(values []string) string {
	if values == nil {
		return "[]"
	}
	b, err := json.Marshal(values)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeSelection(value string) config.TrackSelection {
	var selection config.TrackSelection
	_ = json.Unmarshal([]byte(value), &selection)
	return selection
}

func encodeSelection(selection config.TrackSelection) string {
	b, err := json.Marshal(selection)
	if err != nil {
		return "{}"
	}
	return string(b)
}

type ChannelRow struct {
	Channel   config.Channel
	SortOrder int
	Revision  int64
	UpdatedAt int64
}

func scanChannelRow(row interface {
	Scan(dest ...any) error
}) (ChannelRow, error) {
	var ch config.Channel
	var disabled, onDemand, autostart, restart, upgradeInsecure int
	var headers, preferredAudioLanguages, selection string
	var sort, revision, updated int64
	err := row.Scan(
		&ch.ID, &ch.Title, &ch.Group, &ch.LogoURL, &ch.EPGID, &ch.EPGName, &ch.EPGSource, &ch.Upstream, &ch.Path, &ch.SourceURL, &ch.Ingress,
		&disabled, &onDemand, &autostart, &ch.IdleTimeoutSec, &ch.MaxViewers, &ch.UserAgent,
		&headers, &restart, &ch.PreferHeight, &ch.Packager, &preferredAudioLanguages, &selection, &upgradeInsecure, &sort, &revision, &updated,
	)
	if err != nil {
		return ChannelRow{}, err
	}
	ch.Disabled = intBool(disabled)
	ch.OnDemand = intBool(onDemand)
	ch.Autostart = intBool(autostart)
	ch.RestartOnFailure = intBool(restart)
	ch.UpgradeInsecureRedirects = intBool(upgradeInsecure)
	ch.Headers = decodeHeaders(headers)
	ch.PreferredAudioLanguages = decodeStrings(preferredAudioLanguages)
	ch.Selection = decodeSelection(selection)
	return ChannelRow{Channel: ch, SortOrder: int(sort), Revision: revision, UpdatedAt: updated}, nil
}

func (db *DB) ListChannelRows(includeDisabled bool) ([]ChannelRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	q := `SELECT id, title, group_name, logo_url, epg_id, epg_name, epg_source, upstream, path, source_url, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, user_agent, headers_json, restart_on_failure, prefer_height, packager,
		preferred_audio_languages_json, selection_json, upgrade_insecure_redirects, sort_order, revision, updated_at
		FROM channels`
	if !includeDisabled {
		q += ` WHERE disabled = 0`
	}
	q += ` ORDER BY sort_order ASC, id ASC`
	rows, err := db.sql.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChannelRow
	for rows.Next() {
		ch, err := scanChannelRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ch)
	}
	return out, rows.Err()
}

func (db *DB) ListChannels(includeDisabled bool) ([]config.Channel, error) {
	rows, err := db.ListChannelRows(includeDisabled)
	if err != nil {
		return nil, err
	}
	out := make([]config.Channel, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Channel)
	}
	return out, nil
}

func (db *DB) GetChannel(id string) (config.Channel, bool, error) {
	row, ok, err := db.GetChannelRow(id)
	return row.Channel, ok, err
}

func (db *DB) GetChannelRow(id string) (ChannelRow, bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	row := db.sql.QueryRow(`SELECT id, title, group_name, logo_url, epg_id, epg_name, epg_source, upstream, path, source_url, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, user_agent, headers_json, restart_on_failure, prefer_height, packager,
		preferred_audio_languages_json, selection_json, upgrade_insecure_redirects, sort_order, revision, updated_at
		FROM channels WHERE id = ?`, id)
	ch, err := scanChannelRow(row)
	if err == sql.ErrNoRows {
		return ChannelRow{}, false, nil
	}
	if err != nil {
		return ChannelRow{}, false, err
	}
	return ch, true, nil
}

func (db *DB) UpsertChannel(ch config.Channel) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertChannel(ch, 0)
}

func (db *DB) UpsertChannelIfRevision(ch config.Channel, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertChannel(ch, expectedRevision)
}

func (db *DB) UpsertChannelsIfRevisions(channels []config.Channel, revisions map[string]int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var maxSort int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM channels`).Scan(&maxSort); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, ch := range channels {
		expected := revisions[ch.ID]
		if expected == 0 {
			maxSort++
			if err := insertChannelTx(tx, ch, maxSort, now); err != nil {
				return ErrRevisionConflict
			}
			continue
		}
		result, err := tx.Exec(`UPDATE channels SET
			title=?, group_name=?, logo_url=?, epg_id=?, epg_name=?, epg_source=?, upstream=?, path=?, source_url=?, ingress=?, disabled=?, on_demand=?, autostart=?,
			idle_timeout_sec=?, max_viewers=?, user_agent=?, headers_json=?, restart_on_failure=?,
			prefer_height=?, packager=?, preferred_audio_languages_json=?, selection_json=?, upgrade_insecure_redirects=?, revision=revision+1, updated_at=? WHERE id=? AND revision=?`,
			ch.Title, ch.Group, ch.LogoURL, ch.EPGID, ch.EPGName, ch.EPGSource, ch.Upstream, ch.Path, ch.SourceURL, ch.Ingress, boolInt(ch.Disabled),
			boolInt(ch.OnDemand), boolInt(ch.Autostart), ch.IdleTimeoutSec, ch.MaxViewers, ch.UserAgent,
			encodeHeaders(ch.Headers), boolInt(ch.RestartOnFailure), ch.PreferHeight,
			ch.Packager, encodeStrings(ch.PreferredAudioLanguages), encodeSelection(ch.Selection), boolInt(ch.UpgradeInsecureRedirects), now, ch.ID, expected)
		if err != nil {
			return err
		}
		if err := revisionResult(result); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) upsertChannel(ch config.Channel, expectedRevision int64) error {
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE channels SET
			title=?, group_name=?, logo_url=?, epg_id=?, epg_name=?, epg_source=?, upstream=?, path=?, source_url=?, ingress=?, disabled=?, on_demand=?, autostart=?,
			idle_timeout_sec=?, max_viewers=?, user_agent=?, headers_json=?, restart_on_failure=?,
			prefer_height=?, packager=?, preferred_audio_languages_json=?, selection_json=?, upgrade_insecure_redirects=?, revision=revision+1, updated_at=?
			WHERE id=? AND revision=?`,
			ch.Title, ch.Group, ch.LogoURL, ch.EPGID, ch.EPGName, ch.EPGSource, ch.Upstream, ch.Path, ch.SourceURL, ch.Ingress,
			boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart),
			ch.IdleTimeoutSec, ch.MaxViewers, ch.UserAgent, encodeHeaders(ch.Headers),
			boolInt(ch.RestartOnFailure), ch.PreferHeight, ch.Packager, encodeStrings(ch.PreferredAudioLanguages), encodeSelection(ch.Selection),
			boolInt(ch.UpgradeInsecureRedirects), now, ch.ID, expectedRevision,
		)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	var maxSort int
	_ = db.sql.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM channels`).Scan(&maxSort)
	_, err := db.sql.Exec(`INSERT INTO channels(
		id, title, group_name, logo_url, epg_id, epg_name, epg_source, upstream, path, source_url, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, user_agent, headers_json, restart_on_failure, prefer_height, packager,
		preferred_audio_languages_json, selection_json, upgrade_insecure_redirects, sort_order, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET
		title=excluded.title, group_name=excluded.group_name, logo_url=excluded.logo_url,
		epg_id=excluded.epg_id, epg_name=excluded.epg_name, epg_source=excluded.epg_source,
		upstream=excluded.upstream, path=excluded.path, source_url=excluded.source_url, ingress=excluded.ingress,
		disabled=excluded.disabled, on_demand=excluded.on_demand, autostart=excluded.autostart,
		idle_timeout_sec=excluded.idle_timeout_sec, max_viewers=excluded.max_viewers,
		user_agent=excluded.user_agent, headers_json=excluded.headers_json,
		restart_on_failure=excluded.restart_on_failure, prefer_height=excluded.prefer_height,
		packager=excluded.packager, preferred_audio_languages_json=excluded.preferred_audio_languages_json,
		selection_json=excluded.selection_json, upgrade_insecure_redirects=excluded.upgrade_insecure_redirects,
		revision=channels.revision+1, updated_at=excluded.updated_at`,
		ch.ID, ch.Title, ch.Group, ch.LogoURL, ch.EPGID, ch.EPGName, ch.EPGSource, ch.Upstream, ch.Path, ch.SourceURL, ch.Ingress,
		boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart),
		ch.IdleTimeoutSec, ch.MaxViewers, ch.UserAgent, encodeHeaders(ch.Headers),
		boolInt(ch.RestartOnFailure), ch.PreferHeight, ch.Packager, encodeStrings(ch.PreferredAudioLanguages), encodeSelection(ch.Selection),
		boolInt(ch.UpgradeInsecureRedirects), maxSort+1, now,
	)
	return err
}

func revisionResult(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return ErrRevisionConflict
	}
	return nil
}

func (db *DB) ReorderChannels(ids []string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	for i, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, err := tx.Exec(`UPDATE channels SET sort_order = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ReorderChannelsIfRevisions(ids []string, revisions map[string]int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		expected := revisions[id]
		if expected == 0 {
			return ErrRevisionConflict
		}
		var current int64
		if err := tx.QueryRow(`SELECT revision FROM channels WHERE id = ?`, id).Scan(&current); err != nil {
			return err
		}
		if current != expected {
			return ErrRevisionConflict
		}
	}
	now := time.Now().Unix()
	for i, id := range ids {
		if _, err := tx.Exec(`UPDATE channels SET sort_order=?, revision=revision+1, updated_at=? WHERE id=?`, i, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) DeleteChannel(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`DELETE FROM channels WHERE id = ?`, id)
	return err
}

func (db *DB) DeleteChannelIfRevision(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	result, err := db.sql.Exec(`DELETE FROM channels WHERE id = ? AND revision = ?`, id, expectedRevision)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

func (db *DB) SetAllChannelsDisabled(disabled bool) ([]string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.Query(`SELECT id FROM channels WHERE disabled != ? ORDER BY sort_order, id`, boolInt(disabled))
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		if _, err := tx.Exec(`UPDATE channels SET disabled=?, revision=revision+1, updated_at=? WHERE disabled != ?`,
			boolInt(disabled), time.Now().Unix(), boolInt(disabled)); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return ids, nil
}

type EPGSourceRow struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	URL       string `json:"url,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Proxy     string `json:"proxy"`
	Enabled   bool   `json:"enabled"`
	Deleted   bool   `json:"deleted,omitempty"`
	Revision  int64  `json:"revision"`
	UpdatedAt int64  `json:"updated_at"`
}

func (db *DB) ListEPGSources() ([]EPGSourceRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, name, url, timezone, proxy, enabled, deleted, revision, updated_at
		FROM epg_sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EPGSourceRow{}
	for rows.Next() {
		var row EPGSourceRow
		var enabled, deleted int
		if err := rows.Scan(&row.ID, &row.Name, &row.URL, &row.Timezone, &row.Proxy, &enabled, &deleted, &row.Revision, &row.UpdatedAt); err != nil {
			return nil, err
		}
		row.Enabled = intBool(enabled)
		row.Deleted = intBool(deleted)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (db *DB) UpsertEPGSource(row EPGSourceRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertEPGSource(row, 0)
}

func (db *DB) UpsertEPGSourceIfRevision(row EPGSourceRow, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.upsertEPGSource(row, expectedRevision)
}

func (db *DB) upsertEPGSource(row EPGSourceRow, expectedRevision int64) error {
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE epg_sources SET name=?, url=?, timezone=?, proxy=?, enabled=?,
			deleted=?, revision=revision+1, updated_at=? WHERE id=? AND revision=?`,
			row.Name, row.URL, row.Timezone, row.Proxy, boolInt(row.Enabled), boolInt(row.Deleted), now, row.ID, expectedRevision)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	_, err := db.sql.Exec(`INSERT INTO epg_sources(id, name, url, timezone, proxy, enabled, deleted, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, url=excluded.url, timezone=excluded.timezone,
		proxy=excluded.proxy, enabled=excluded.enabled, deleted=excluded.deleted, revision=epg_sources.revision+1,
		updated_at=excluded.updated_at`,
		row.ID, row.Name, row.URL, row.Timezone, row.Proxy, boolInt(row.Enabled), boolInt(row.Deleted), now)
	return err
}

func (db *DB) HideEPGSourceIfRevision(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE epg_sources SET enabled=0, deleted=1,
			revision=revision+1, updated_at=? WHERE id=? AND revision=?`, now, id, expectedRevision)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	result, err := db.sql.Exec(`INSERT INTO epg_sources(id, proxy, enabled, deleted, updated_at)
		VALUES (?, 'direct', 0, 1, ?) ON CONFLICT(id) DO NOTHING`, id, now)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

func (db *DB) DeleteEPGSourceIfRevision(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	result, err := db.sql.Exec(`DELETE FROM epg_sources WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

type AccessLogRow struct {
	ID          int64  `json:"id"`
	TokenID     string `json:"token_id"`
	TokenPrefix string `json:"token_prefix"`
	Path        string `json:"path"`
	ChannelID   string `json:"channel_id,omitempty"`
	Status      int    `json:"status"`
	Remote      string `json:"remote,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func (db *DB) InsertAccessLog(row AccessLogRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if row.CreatedAt == 0 {
		row.CreatedAt = time.Now().Unix()
	}
	_, err := db.sql.Exec(`INSERT INTO access_logs(token_id, token_prefix, path, channel_id, status, remote, created_at)
		VALUES (?,?,?,?,?,?,?)`,
		row.TokenID, row.TokenPrefix, row.Path, row.ChannelID, row.Status, row.Remote, row.CreatedAt,
	)
	if err != nil {
		return err
	}
	_, _ = db.sql.Exec(`DELETE FROM access_logs WHERE id NOT IN (
		SELECT id FROM access_logs ORDER BY id DESC LIMIT 5000
	)`)
	return nil
}

func (db *DB) ListAccessLogs(limit int, tokenID string) ([]AccessLogRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if tokenID != "" {
		rows, err = db.sql.Query(`SELECT id, token_id, token_prefix, path, channel_id, status, remote, created_at
			FROM access_logs WHERE token_id = ? ORDER BY id DESC LIMIT ?`, tokenID, limit)
	} else {
		rows, err = db.sql.Query(`SELECT id, token_id, token_prefix, path, channel_id, status, remote, created_at
			FROM access_logs ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessLogRow
	for rows.Next() {
		var r AccessLogRow
		if err := rows.Scan(&r.ID, &r.TokenID, &r.TokenPrefix, &r.Path, &r.ChannelID, &r.Status, &r.Remote, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) DeleteAccessLogsBefore(cutoff int64) (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	result, err := db.sql.Exec(`DELETE FROM access_logs WHERE created_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (db *DB) ClearAccessLogs() (int64, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	result, err := db.sql.Exec(`DELETE FROM access_logs`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type AccessTokenRow struct {
	ID         string
	Name       string
	TokenHash  string
	Prefix     string
	ScopeJSON  string
	Enabled    bool
	Note       string
	CreatedAt  int64
	ExpiresAt  int64
	LastUsedAt int64
	RevokedAt  int64
	Revision   int64
	UpdatedAt  int64
}

func (db *DB) InsertAccessToken(row AccessTokenRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`INSERT INTO access_tokens(
		id, name, token_hash, token_prefix, scope_json, enabled, note, created_at, expires_at, last_used_at, revoked_at, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.ID, row.Name, row.TokenHash, row.Prefix, row.ScopeJSON, boolInt(row.Enabled), row.Note,
		row.CreatedAt, row.ExpiresAt, row.LastUsedAt, row.RevokedAt, nonzeroTime(row.UpdatedAt, row.CreatedAt),
	)
	if err == nil {
		db.invalidateAccessTokenCache(row.ID)
	}
	return err
}

func (db *DB) ListAccessTokens() ([]AccessTokenRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, name, token_hash, token_prefix, scope_json, enabled, note, created_at, expires_at, last_used_at, revoked_at, revision, updated_at
		FROM access_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessTokenRow
	for rows.Next() {
		var r AccessTokenRow
		var en int
		if err := rows.Scan(&r.ID, &r.Name, &r.TokenHash, &r.Prefix, &r.ScopeJSON, &en, &r.Note, &r.CreatedAt, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt, &r.Revision, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = intBool(en)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (db *DB) GetAccessTokenByHash(hash string) (AccessTokenRow, bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if row, ok := db.accessTokens[hash]; ok {
		return row, true, nil
	}
	var r AccessTokenRow
	var en int
	err := db.sql.QueryRow(`SELECT id, name, token_hash, token_prefix, scope_json, enabled, note, created_at, expires_at, last_used_at, revoked_at, revision, updated_at
		FROM access_tokens WHERE token_hash = ?`, hash).Scan(
		&r.ID, &r.Name, &r.TokenHash, &r.Prefix, &r.ScopeJSON, &en, &r.Note, &r.CreatedAt, &r.ExpiresAt, &r.LastUsedAt, &r.RevokedAt, &r.Revision, &r.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return AccessTokenRow{}, false, nil
	}
	if err != nil {
		return AccessTokenRow{}, false, err
	}
	r.Enabled = intBool(en)
	if db.accessTokens == nil {
		db.accessTokens = make(map[string]AccessTokenRow)
	}
	db.accessTokens[hash] = r
	return r, true, nil
}

func (db *DB) TouchAccessToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	now := time.Now()
	if touched := db.accessTokenTouches[id]; !touched.IsZero() && now.Sub(touched) < accessTokenTouchInterval {
		return nil
	}
	_, err := db.sql.Exec(`UPDATE access_tokens SET last_used_at = ? WHERE id = ? AND last_used_at < ?`,
		now.Unix(), id, now.Add(-accessTokenTouchInterval).Unix())
	if err == nil {
		if db.accessTokenTouches == nil {
			db.accessTokenTouches = make(map[string]time.Time)
		}
		db.accessTokenTouches[id] = now
		for hash, row := range db.accessTokens {
			if row.ID == id && row.LastUsedAt < now.Add(-accessTokenTouchInterval).Unix() {
				row.LastUsedAt = now.Unix()
				db.accessTokens[hash] = row
			}
		}
	}
	return err
}

func (db *DB) RevokeAccessToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	err := db.revokeAccessToken(id, 0)
	if err == nil {
		db.invalidateAccessTokenCache(id)
	}
	return err
}

func (db *DB) RevokeAccessTokenIfRevision(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	err := db.revokeAccessToken(id, expectedRevision)
	if err == nil {
		db.invalidateAccessTokenCache(id)
	}
	return err
}

func (db *DB) revokeAccessToken(id string, expectedRevision int64) error {
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE access_tokens SET revoked_at=?, enabled=0, revision=revision+1, updated_at=?
			WHERE id=? AND revision=? AND revoked_at=0`, now, now, id, expectedRevision)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	_, err := db.sql.Exec(`UPDATE access_tokens SET revoked_at=?, enabled=0, revision=revision+1, updated_at=?
		WHERE id=? AND revoked_at=0`, now, now, id)
	return err
}

func (db *DB) DeleteAccessToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`DELETE FROM access_tokens WHERE id = ?`, id)
	if err == nil {
		db.invalidateAccessTokenCache(id)
	}
	return err
}

func (db *DB) DeleteAccessTokenIfRevision(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	result, err := db.sql.Exec(`DELETE FROM access_tokens WHERE id = ? AND revision = ?`, id, expectedRevision)
	if err != nil {
		return err
	}
	if err := revisionResult(result); err != nil {
		return err
	}
	db.invalidateAccessTokenCache(id)
	return nil
}

func (db *DB) invalidateAccessTokenCache(id string) {
	for hash, row := range db.accessTokens {
		if row.ID == id {
			delete(db.accessTokens, hash)
		}
	}
	delete(db.accessTokenTouches, id)
}

type AdminAPITokenRow struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	TokenHash  string `json:"-"`
	Prefix     string `json:"token_prefix"`
	ScopeJSON  string `json:"-"`
	Enabled    bool   `json:"enabled"`
	Note       string `json:"note,omitempty"`
	CreatedBy  string `json:"created_by,omitempty"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at,omitempty"`
	LastUsedAt int64  `json:"last_used_at,omitempty"`
	RevokedAt  int64  `json:"revoked_at,omitempty"`
	Revision   int64  `json:"revision"`
	UpdatedAt  int64  `json:"updated_at"`
}

func (db *DB) InsertAdminAPIToken(row AdminAPITokenRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`INSERT INTO admin_api_tokens(
		id, name, token_hash, token_prefix, scopes_json, enabled, note, created_by,
		created_at, expires_at, last_used_at, revoked_at, revision, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.ID, row.Name, row.TokenHash, row.Prefix, row.ScopeJSON, boolInt(row.Enabled),
		row.Note, row.CreatedBy, row.CreatedAt, row.ExpiresAt, row.LastUsedAt, row.RevokedAt,
		nonzeroRevision(row.Revision), nonzeroTime(row.UpdatedAt, row.CreatedAt),
	)
	return err
}

func (db *DB) ListAdminAPITokens() ([]AdminAPITokenRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, name, token_hash, token_prefix, scopes_json, enabled,
		note, created_by, created_at, expires_at, last_used_at, revoked_at, revision, updated_at
		FROM admin_api_tokens ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminAPITokenRow{}
	for rows.Next() {
		row, err := scanAdminAPIToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (db *DB) GetAdminAPITokenByHash(hash string) (AdminAPITokenRow, bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	row, err := scanAdminAPIToken(db.sql.QueryRow(`SELECT id, name, token_hash, token_prefix,
		scopes_json, enabled, note, created_by, created_at, expires_at, last_used_at,
		revoked_at, revision, updated_at FROM admin_api_tokens WHERE token_hash=?`, hash))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminAPITokenRow{}, false, nil
	}
	return row, err == nil, err
}

func (db *DB) GetAdminAPITokenByID(id string) (AdminAPITokenRow, bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	row, err := scanAdminAPIToken(db.sql.QueryRow(`SELECT id, name, token_hash, token_prefix,
		scopes_json, enabled, note, created_by, created_at, expires_at, last_used_at,
		revoked_at, revision, updated_at FROM admin_api_tokens WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminAPITokenRow{}, false, nil
	}
	return row, err == nil, err
}

func scanAdminAPIToken(row interface{ Scan(...any) error }) (AdminAPITokenRow, error) {
	var token AdminAPITokenRow
	var enabled int
	err := row.Scan(
		&token.ID, &token.Name, &token.TokenHash, &token.Prefix, &token.ScopeJSON, &enabled,
		&token.Note, &token.CreatedBy, &token.CreatedAt, &token.ExpiresAt, &token.LastUsedAt,
		&token.RevokedAt, &token.Revision, &token.UpdatedAt,
	)
	token.Enabled = intBool(enabled)
	return token, err
}

func (db *DB) UpdateAdminAPIToken(row AdminAPITokenRow, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if expectedRevision <= 0 {
		return ErrRevisionConflict
	}
	result, err := db.sql.Exec(`UPDATE admin_api_tokens SET name=?, scopes_json=?, enabled=?,
		note=?, expires_at=?, revision=revision+1, updated_at=?
		WHERE id=? AND revision=? AND revoked_at=0`, row.Name, row.ScopeJSON, boolInt(row.Enabled),
		row.Note, row.ExpiresAt, time.Now().Unix(), row.ID, expectedRevision)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

func (db *DB) RotateAdminAPIToken(id, tokenHash, tokenPrefix string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if expectedRevision <= 0 {
		return ErrRevisionConflict
	}
	result, err := db.sql.Exec(`UPDATE admin_api_tokens SET token_hash=?, token_prefix=?,
		last_used_at=0, revision=revision+1, updated_at=? WHERE id=? AND revision=? AND
		enabled=1 AND revoked_at=0`, tokenHash, tokenPrefix, time.Now().Unix(), id, expectedRevision)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

func (db *DB) TouchAdminAPIToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`UPDATE admin_api_tokens SET last_used_at=? WHERE id=?`, time.Now().Unix(), id)
	return err
}

func (db *DB) RevokeAdminAPIToken(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if expectedRevision <= 0 {
		return ErrRevisionConflict
	}
	now := time.Now().Unix()
	result, err := db.sql.Exec(`UPDATE admin_api_tokens SET enabled=0, revoked_at=?,
		revision=revision+1, updated_at=? WHERE id=? AND revision=? AND revoked_at=0`,
		now, now, id, expectedRevision)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

func (db *DB) DeleteAdminAPIToken(id string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if expectedRevision <= 0 {
		return ErrRevisionConflict
	}
	result, err := db.sql.Exec(`DELETE FROM admin_api_tokens WHERE id=? AND revision=?`, id, expectedRevision)
	if err != nil {
		return err
	}
	return revisionResult(result)
}

type AdminAPITokenLogRow struct {
	ID          int64  `json:"id"`
	TokenID     string `json:"token_id"`
	TokenPrefix string `json:"token_prefix"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Scope       string `json:"required_scope"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
	Status      int    `json:"status"`
	Remote      string `json:"remote,omitempty"`
	UserAgent   string `json:"user_agent,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	CreatedAt   int64  `json:"created_at"`
}

func (db *DB) InsertAdminAPITokenLog(row AdminAPITokenLogRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if row.CreatedAt == 0 {
		row.CreatedAt = time.Now().Unix()
	}
	_, err := db.sql.Exec(`INSERT INTO admin_api_token_logs(
		token_id, token_prefix, method, path, required_scope, decision, reason, status,
		remote, user_agent, request_id, created_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, row.TokenID, row.TokenPrefix, row.Method, row.Path,
		row.Scope, row.Decision, row.Reason, row.Status, row.Remote, row.UserAgent,
		row.RequestID, row.CreatedAt)
	if err != nil {
		return err
	}
	_, _ = db.sql.Exec(`DELETE FROM admin_api_token_logs WHERE id NOT IN (
		SELECT id FROM admin_api_token_logs ORDER BY id DESC LIMIT 5000
	)`)
	return nil
}

func (db *DB) ListAdminAPITokenLogs(limit int) ([]AdminAPITokenLogRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.sql.Query(`SELECT id, COALESCE(token_id, ''), token_prefix, method, path,
		required_scope, decision, reason, status, remote, user_agent, request_id, created_at
		FROM admin_api_token_logs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AdminAPITokenLogRow{}
	for rows.Next() {
		var row AdminAPITokenLogRow
		if err := rows.Scan(&row.ID, &row.TokenID, &row.TokenPrefix, &row.Method,
			&row.Path, &row.Scope, &row.Decision, &row.Reason, &row.Status, &row.Remote,
			&row.UserAgent, &row.RequestID, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func nonzeroRevision(value int64) int64 {
	if value > 0 {
		return value
	}
	return 1
}

func (db *DB) GetSetting(key string) (string, bool, error) {
	row, ok, err := db.GetSettingRow(key)
	return row.Value, ok, err
}

type SettingRow struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	Revision  int64  `json:"revision"`
	UpdatedAt int64  `json:"updated_at"`
}

func (db *DB) GetSettingRow(key string) (SettingRow, bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	var row SettingRow
	err := db.sql.QueryRow(`SELECT key, value, revision, updated_at FROM settings WHERE key = ?`, key).Scan(
		&row.Key, &row.Value, &row.Revision, &row.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return SettingRow{}, false, nil
	}
	if err != nil {
		return SettingRow{}, false, err
	}
	return row, true, nil
}

func (db *DB) SetSetting(key, value string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.setSetting(key, value, 0)
}

func (db *DB) SetSettingIfRevision(key, value string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	return db.setSetting(key, value, expectedRevision)
}

func (db *DB) setSetting(key, value string, expectedRevision int64) error {
	now := time.Now().Unix()
	if expectedRevision > 0 {
		result, err := db.sql.Exec(`UPDATE settings SET value=?, revision=revision+1, updated_at=?
			WHERE key=? AND revision=?`, value, now, key, expectedRevision)
		if err != nil {
			return err
		}
		return revisionResult(result)
	}
	_, err := db.sql.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, revision=settings.revision+1,
		updated_at=excluded.updated_at`, key, value, now)
	return err
}

func (db *DB) ReplaceRuntimeSettings(publicBaseURL, retentionDays string, expectedRevision int64) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().Unix()
	result, err := tx.Exec(`UPDATE settings SET revision=revision+1, updated_at=?
		WHERE key='runtime_settings_revision' AND revision=?`, now, expectedRevision)
	if err != nil {
		return err
	}
	if err := revisionResult(result); err != nil {
		return err
	}
	for key, value := range map[string]string{
		"public_base_url":           publicBaseURL,
		"access_log_retention_days": retentionDays,
	} {
		if _, err := tx.Exec(`INSERT INTO settings(key, value, updated_at) VALUES (?,?,?)
			ON CONFLICT(key) DO UPDATE SET value=excluded.value, revision=settings.revision+1,
			updated_at=excluded.updated_at`, key, value, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (db *DB) ListSettings() (map[string]string, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func ValidateChannel(ch config.Channel, upstreams []config.Upstream, hasGlobalKeys bool) error {
	if err := config.ValidateChannelID(ch.ID); err != nil {
		return fmt.Errorf("id: %w", err)
	}
	if ch.SourceURL != "" {
		if err := config.ValidateSourceURL(ch.SourceURL); err != nil {
			return fmt.Errorf("source_url: %w", err)
		}
	} else {
		if ch.Upstream == "" {
			return fmt.Errorf("upstream required")
		}
		found := false
		for _, u := range upstreams {
			if u.ID == ch.Upstream {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unknown upstream %q", ch.Upstream)
		}
		if ch.Path == "" {
			return fmt.Errorf("path required")
		}
	}
	ch.Ingress = strings.ToLower(ch.Ingress)
	if ch.Ingress != "hls" && ch.Ingress != "dash" {
		return fmt.Errorf("ingress must be hls or dash")
	}
	if ch.Ingress == "dash" && !ch.Disabled && !hasGlobalKeys {
		return fmt.Errorf("dash requires global packager.keys_file")
	}
	if err := config.ValidateTrackSelection(ch.Selection); err != nil {
		return fmt.Errorf("selection: %w", err)
	}
	if err := config.ValidateEngineSelection(ch.Packager, ch.Selection); err != nil {
		return fmt.Errorf("selection: %w", err)
	}
	return nil
}
