package store

import (
	"database/sql"
	"encoding/json"
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
	sql *sql.DB
	mu  sync.Mutex
}

func Open(dataDir string) (*DB, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, "kiln.db")
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
	return db, nil
}

func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

func (db *DB) migrate() error {
	const schema = `
CREATE TABLE IF NOT EXISTS schema_version (
  version INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS channels (
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
CREATE TABLE IF NOT EXISTS access_tokens (
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
CREATE INDEX IF NOT EXISTS idx_access_tokens_hash ON access_tokens(token_hash);
CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS access_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id TEXT NOT NULL DEFAULT '',
  token_prefix TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  channel_id TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 0,
  remote TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_access_logs_created ON access_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_access_logs_token ON access_logs(token_id, created_at DESC);
`
	if _, err := db.sql.Exec(schema); err != nil {
		return err
	}
	var n int
	if err := db.sql.QueryRow(`SELECT COUNT(1) FROM schema_version`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if _, err := db.sql.Exec(`INSERT INTO schema_version(version) VALUES (1)`); err != nil {
			return err
		}
	}
	if _, err := db.sql.Exec(`CREATE TABLE IF NOT EXISTS access_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  token_id TEXT NOT NULL DEFAULT '',
  token_prefix TEXT NOT NULL DEFAULT '',
  path TEXT NOT NULL DEFAULT '',
  channel_id TEXT NOT NULL DEFAULT '',
  status INTEGER NOT NULL DEFAULT 0,
  remote TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
)`); err != nil {
		return err
	}
	if _, err := db.sql.Exec(`
CREATE TABLE IF NOT EXISTS proxy_profiles (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL,
  disabled INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS proxy_rules (
  id TEXT PRIMARY KEY,
  priority INTEGER NOT NULL DEFAULT 100,
  kind TEXT NOT NULL DEFAULT 'host_suffix',
  pattern TEXT NOT NULL DEFAULT '',
  proxy_id TEXT NOT NULL DEFAULT 'direct',
  disabled INTEGER NOT NULL DEFAULT 0
);
`); err != nil {
		return err
	}
	return nil
}

func (db *DB) SeedFromConfig(cfg config.File) error {
	if err := db.seedChannels(cfg); err != nil {
		return err
	}
	return db.seedEgress(cfg)
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
		if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES ('public_base_url', ?)`, cfg.Server.PublicBaseURL); err != nil {
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
	if n > 0 {
		return nil
	}
	tx, err := db.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, p := range cfg.Proxies {
		if _, err := tx.Exec(`INSERT INTO proxy_profiles(id, name, url, disabled) VALUES (?,?,?,?)`,
			p.ID, p.Name, p.URL, boolInt(p.Disabled)); err != nil {
			return err
		}
	}
	for i, r := range cfg.Egress.Rules {
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("rule-%d", i+1)
		}
		if _, err := tx.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled) VALUES (?,?,?,?,?,?)`,
			id, r.Priority, r.Kind, r.Pattern, r.Proxy, boolInt(r.Disabled)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES ('egress_default', ?)`, cfg.Egress.Default); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES ('playlist_policy', ?)`, cfg.Egress.PlaylistPolicy); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO settings(key, value) VALUES ('docker_proxy_host', ?)`, cfg.Egress.DockerProxyHost); err != nil {
		return err
	}
	return tx.Commit()
}

type ProxyProfileRow struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url"`
	Disabled bool   `json:"disabled"`
}

type ProxyRuleRow struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
	Kind     string `json:"kind"`
	Pattern  string `json:"pattern"`
	ProxyID  string `json:"proxy"`
	Disabled bool   `json:"disabled"`
}

func (db *DB) ListProxyProfiles() ([]ProxyProfileRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, name, url, disabled FROM proxy_profiles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyProfileRow
	for rows.Next() {
		var r ProxyProfileRow
		var dis int
		if err := rows.Scan(&r.ID, &r.Name, &r.URL, &dis); err != nil {
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
	_, err := db.sql.Exec(`INSERT INTO proxy_profiles(id, name, url, disabled) VALUES (?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, url=excluded.url, disabled=excluded.disabled`,
		p.ID, p.Name, p.URL, boolInt(p.Disabled))
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
	rows, err := db.sql.Query(`SELECT id, priority, kind, pattern, proxy_id, disabled FROM proxy_rules ORDER BY priority ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProxyRuleRow
	for rows.Next() {
		var r ProxyRuleRow
		var dis int
		if err := rows.Scan(&r.ID, &r.Priority, &r.Kind, &r.Pattern, &r.ProxyID, &dis); err != nil {
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
	if r.ID == "" {
		return fmt.Errorf("rule id required")
	}
	if r.Kind == "" {
		r.Kind = "host_suffix"
	}
	if r.ProxyID == "" {
		r.ProxyID = "direct"
	}
	_, err := db.sql.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled) VALUES (?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET priority=excluded.priority, kind=excluded.kind, pattern=excluded.pattern,
		proxy_id=excluded.proxy_id, disabled=excluded.disabled`,
		r.ID, r.Priority, r.Kind, r.Pattern, r.ProxyID, boolInt(r.Disabled))
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
		if _, err := tx.Exec(`INSERT INTO proxy_rules(id, priority, kind, pattern, proxy_id, disabled) VALUES (?,?,?,?,?,?)`,
			r.ID, r.Priority, r.Kind, r.Pattern, r.ProxyID, boolInt(r.Disabled)); err != nil {
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
		id, title, group_name, logo_url, upstream, path, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, keys_file, user_agent, headers_json, restart_on_failure, prefer_height, sort_order, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		ch.ID, ch.Title, ch.Group, ch.LogoURL, ch.Upstream, ch.Path, ch.Ingress,
		boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart),
		ch.IdleTimeoutSec, ch.MaxViewers, ch.KeysFile, ch.UserAgent, string(hj),
		boolInt(ch.RestartOnFailure), ch.PreferHeight, sort, now,
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

type ChannelRow struct {
	Channel   config.Channel
	SortOrder int
}

func scanChannelRow(row interface {
	Scan(dest ...any) error
}) (ChannelRow, error) {
	var ch config.Channel
	var disabled, onDemand, autostart, restart int
	var headers string
	var sort, updated int64
	err := row.Scan(
		&ch.ID, &ch.Title, &ch.Group, &ch.LogoURL, &ch.Upstream, &ch.Path, &ch.Ingress,
		&disabled, &onDemand, &autostart, &ch.IdleTimeoutSec, &ch.MaxViewers, &ch.KeysFile, &ch.UserAgent,
		&headers, &restart, &ch.PreferHeight, &sort, &updated,
	)
	if err != nil {
		return ChannelRow{}, err
	}
	ch.Disabled = intBool(disabled)
	ch.OnDemand = intBool(onDemand)
	ch.Autostart = intBool(autostart)
	ch.RestartOnFailure = intBool(restart)
	ch.Headers = decodeHeaders(headers)
	return ChannelRow{Channel: ch, SortOrder: int(sort)}, nil
}

func (db *DB) ListChannelRows(includeDisabled bool) ([]ChannelRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	q := `SELECT id, title, group_name, logo_url, upstream, path, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, keys_file, user_agent, headers_json, restart_on_failure, prefer_height, sort_order, updated_at
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
	db.mu.Lock()
	defer db.mu.Unlock()
	row := db.sql.QueryRow(`SELECT id, title, group_name, logo_url, upstream, path, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, keys_file, user_agent, headers_json, restart_on_failure, prefer_height, sort_order, updated_at
		FROM channels WHERE id = ?`, id)
	ch, err := scanChannelRow(row)
	if err == sql.ErrNoRows {
		return config.Channel{}, false, nil
	}
	if err != nil {
		return config.Channel{}, false, err
	}
	return ch.Channel, true, nil
}

func (db *DB) UpsertChannel(ch config.Channel) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	now := time.Now().Unix()
	var maxSort int
	_ = db.sql.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM channels`).Scan(&maxSort)
	_, err := db.sql.Exec(`INSERT INTO channels(
		id, title, group_name, logo_url, upstream, path, ingress, disabled, on_demand, autostart,
		idle_timeout_sec, max_viewers, keys_file, user_agent, headers_json, restart_on_failure, prefer_height, sort_order, updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(id) DO UPDATE SET
		title=excluded.title, group_name=excluded.group_name, logo_url=excluded.logo_url,
		upstream=excluded.upstream, path=excluded.path, ingress=excluded.ingress,
		disabled=excluded.disabled, on_demand=excluded.on_demand, autostart=excluded.autostart,
		idle_timeout_sec=excluded.idle_timeout_sec, max_viewers=excluded.max_viewers,
		keys_file=excluded.keys_file, user_agent=excluded.user_agent, headers_json=excluded.headers_json,
		restart_on_failure=excluded.restart_on_failure, prefer_height=excluded.prefer_height,
		updated_at=excluded.updated_at`,
		ch.ID, ch.Title, ch.Group, ch.LogoURL, ch.Upstream, ch.Path, ch.Ingress,
		boolInt(ch.Disabled), boolInt(ch.OnDemand), boolInt(ch.Autostart),
		ch.IdleTimeoutSec, ch.MaxViewers, ch.KeysFile, ch.UserAgent, encodeHeaders(ch.Headers),
		boolInt(ch.RestartOnFailure), ch.PreferHeight, maxSort+1, now,
	)
	return err
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
		if _, err := tx.Exec(`UPDATE channels SET sort_order = ?, updated_at = ? WHERE id = ?`, i, now, id); err != nil {
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

type AccessTokenRow struct {
	ID         string
	Name       string
	TokenHash  string
	Prefix     string
	ScopeJSON  string
	Enabled    bool
	Note       string
	CreatedAt  int64
	LastUsedAt int64
	RevokedAt  int64
}

func (db *DB) InsertAccessToken(row AccessTokenRow) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`INSERT INTO access_tokens(
		id, name, token_hash, token_prefix, scope_json, enabled, note, created_at, last_used_at, revoked_at
	) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		row.ID, row.Name, row.TokenHash, row.Prefix, row.ScopeJSON, boolInt(row.Enabled), row.Note,
		row.CreatedAt, row.LastUsedAt, row.RevokedAt,
	)
	return err
}

func (db *DB) ListAccessTokens() ([]AccessTokenRow, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	rows, err := db.sql.Query(`SELECT id, name, token_hash, token_prefix, scope_json, enabled, note, created_at, last_used_at, revoked_at
		FROM access_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessTokenRow
	for rows.Next() {
		var r AccessTokenRow
		var en int
		if err := rows.Scan(&r.ID, &r.Name, &r.TokenHash, &r.Prefix, &r.ScopeJSON, &en, &r.Note, &r.CreatedAt, &r.LastUsedAt, &r.RevokedAt); err != nil {
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
	var r AccessTokenRow
	var en int
	err := db.sql.QueryRow(`SELECT id, name, token_hash, token_prefix, scope_json, enabled, note, created_at, last_used_at, revoked_at
		FROM access_tokens WHERE token_hash = ?`, hash).Scan(
		&r.ID, &r.Name, &r.TokenHash, &r.Prefix, &r.ScopeJSON, &en, &r.Note, &r.CreatedAt, &r.LastUsedAt, &r.RevokedAt,
	)
	if err == sql.ErrNoRows {
		return AccessTokenRow{}, false, nil
	}
	if err != nil {
		return AccessTokenRow{}, false, err
	}
	r.Enabled = intBool(en)
	return r, true, nil
}

func (db *DB) TouchAccessToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`UPDATE access_tokens SET last_used_at = ? WHERE id = ?`, time.Now().Unix(), id)
	return err
}

func (db *DB) RevokeAccessToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`UPDATE access_tokens SET revoked_at = ?, enabled = 0 WHERE id = ? AND revoked_at = 0`, time.Now().Unix(), id)
	return err
}

func (db *DB) DeleteAccessToken(id string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`DELETE FROM access_tokens WHERE id = ?`, id)
	return err
}

func (db *DB) GetSetting(key string) (string, bool, error) {
	db.mu.Lock()
	defer db.mu.Unlock()
	var v string
	err := db.sql.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (db *DB) SetSetting(key, value string) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	_, err := db.sql.Exec(`INSERT INTO settings(key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
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

func ValidateChannel(ch config.Channel, upstreams []config.Upstream) error {
	if ch.ID == "" {
		return fmt.Errorf("id required")
	}
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
	ch.Ingress = strings.ToLower(ch.Ingress)
	if ch.Ingress != "hls" && ch.Ingress != "dash" {
		return fmt.Errorf("ingress must be hls or dash")
	}
	if ch.Path == "" {
		return fmt.Errorf("path required")
	}
	if ch.Ingress == "dash" && !ch.Disabled && ch.KeysFile == "" {
		return fmt.Errorf("dash requires keys_file")
	}
	return nil
}
