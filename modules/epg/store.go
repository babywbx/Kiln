package epg

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

	_ "modernc.org/sqlite"
)

const storeSchemaVersion = 1

// Bump when ingestion or name normalisation changes so stored rows are rebuilt.
const ingestVersion = "1"

const storeFileName = "epg.db"

type Store struct {
	sql  *sql.DB
	path string
	mu   sync.RWMutex
}

type SourceState struct {
	SourceID       string
	Digest         string
	Metadata       CacheMetadata
	UpdatedAt      time.Time
	ChannelCount   int
	ProgrammeCount int
}

type storedChannel struct {
	SourceID     string
	ChannelID    string
	DisplayNames []Text
	Icons        []Icon
	URLs         []string
}

func NewDiskStore(directory string) (*Store, error) {
	if directory == "" {
		return nil, fmt.Errorf("EPG cache directory is empty")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, fmt.Errorf("create EPG cache directory: %w", err)
	}
	path := filepath.Join(directory, storeFileName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create EPG database: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set EPG database permissions: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close EPG database: %w", err)
	}
	if err := restrictStoreSidecars(path); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	store, err := openStore(dsn, path)
	if err != nil {
		return nil, err
	}
	if err := restrictStoreSidecars(path); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func NewMemoryStore() (*Store, error) {
	return openStore("file::memory:?_pragma=busy_timeout(5000)", "")
}

func openStore(dsn, path string) (*Store, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open EPG database: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	store := &Store{sql: sqlDB, path: path}
	if err := store.migrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return store, nil
}

func restrictStoreSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Chmod(path+suffix, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("set EPG database permissions: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.sql == nil {
		return nil
	}
	return s.sql.Close()
}

func (s *Store) migrate() error {
	tx, err := s.sql.Begin()
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
	if version > storeSchemaVersion {
		return fmt.Errorf("EPG database schema version %d is newer than supported version %d", version, storeSchemaVersion)
	}
	for version < storeSchemaVersion {
		next := version + 1
		if err := applyStoreMigration(tx, next); err != nil {
			return fmt.Errorf("migrate EPG database to version %d: %w", next, err)
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

func applyStoreMigration(tx *sql.Tx, version int) error {
	switch version {
	case 1:
		_, err := tx.Exec(storeSchemaV1)
		return err
	default:
		return fmt.Errorf("unknown EPG schema version %d", version)
	}
}

const storeSchemaV1 = `
CREATE TABLE epg_sources (
  source_id TEXT PRIMARY KEY,
  digest TEXT NOT NULL DEFAULT '',
  etag TEXT NOT NULL DEFAULT '',
  last_modified TEXT NOT NULL DEFAULT '',
  content_type TEXT NOT NULL DEFAULT '',
  cache_control TEXT NOT NULL DEFAULT '',
  fetched_at INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL DEFAULT 0,
  channel_count INTEGER NOT NULL DEFAULT 0,
  programme_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE epg_channels (
  source_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  names_json TEXT NOT NULL DEFAULT '[]',
  icons_json TEXT NOT NULL DEFAULT '[]',
  urls_json TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (source_id, channel_id)
);
CREATE TABLE epg_channel_names (
  source_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  normalized TEXT NOT NULL
);
CREATE INDEX idx_epg_channel_names ON epg_channel_names(source_id, normalized);
CREATE TABLE epg_programmes (
  source_id TEXT NOT NULL,
  channel_id TEXT NOT NULL,
  start_at INTEGER NOT NULL,
  start_raw TEXT NOT NULL,
  stop_raw TEXT NOT NULL DEFAULT '',
  pdc_raw TEXT NOT NULL DEFAULT '',
  vps_raw TEXT NOT NULL DEFAULT '',
  showview TEXT NOT NULL DEFAULT '',
  videoplus TEXT NOT NULL DEFAULT '',
  clumpidx TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_epg_programmes ON epg_programmes(source_id, channel_id, start_at);
`

func (s *Store) states() (map[string]SourceState, error) {
	rows, err := s.sql.Query(`SELECT source_id, digest, etag, last_modified, content_type, cache_control,
	  fetched_at, updated_at, channel_count, programme_count FROM epg_sources`)
	if err != nil {
		return nil, fmt.Errorf("read EPG source state: %w", err)
	}
	defer func() { _ = rows.Close() }()
	states := make(map[string]SourceState)
	for rows.Next() {
		var state SourceState
		var fetchedAt, updatedAt int64
		if err := rows.Scan(&state.SourceID, &state.Digest, &state.Metadata.ETag, &state.Metadata.LastModified,
			&state.Metadata.ContentType, &state.Metadata.CacheControl, &fetchedAt, &updatedAt,
			&state.ChannelCount, &state.ProgrammeCount); err != nil {
			return nil, fmt.Errorf("read EPG source state: %w", err)
		}
		if fetchedAt > 0 {
			state.Metadata.FetchedAt = time.Unix(fetchedAt, 0).UTC()
		}
		if updatedAt > 0 {
			state.UpdatedAt = time.Unix(updatedAt, 0).UTC()
		}
		states[state.SourceID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read EPG source state: %w", err)
	}
	return states, nil
}

func (s *Store) saveMetadata(sourceID string, metadata CacheMetadata) error {
	_, err := s.sql.Exec(`UPDATE epg_sources SET etag = ?, last_modified = ?, content_type = ?,
	  cache_control = ?, fetched_at = ? WHERE source_id = ?`,
		metadata.ETag, metadata.LastModified, metadata.ContentType, metadata.CacheControl,
		unixOrZero(metadata.FetchedAt), sourceID)
	if err != nil {
		return fmt.Errorf("save EPG source metadata %q: %w", sourceID, err)
	}
	return nil
}

func (s *Store) retain(sourceIDs []string) error {
	if len(sourceIDs) == 0 {
		for _, table := range []string{"epg_sources", "epg_channels", "epg_channel_names", "epg_programmes"} {
			if _, err := s.sql.Exec(`DELETE FROM ` + table); err != nil {
				return fmt.Errorf("prune EPG sources: %w", err)
			}
		}
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",")
	arguments := make([]any, 0, len(sourceIDs))
	for _, id := range sourceIDs {
		arguments = append(arguments, id)
	}
	for _, table := range []string{"epg_sources", "epg_channels", "epg_channel_names", "epg_programmes"} {
		if _, err := s.sql.Exec(`DELETE FROM `+table+` WHERE source_id NOT IN (`+placeholders+`)`, arguments...); err != nil {
			return fmt.Errorf("prune EPG sources: %w", err)
		}
	}
	return nil
}

type ingest struct {
	store          *Store
	tx             *sql.Tx
	sourceID       string
	channels       *sql.Stmt
	names          *sql.Stmt
	programmes     *sql.Stmt
	channelCount   int
	programmeCount int
	done           bool
}

func (s *Store) beginIngest(sourceID string) (*ingest, error) {
	tx, err := s.sql.Begin()
	if err != nil {
		return nil, fmt.Errorf("ingest EPG source %q: %w", sourceID, err)
	}
	item := &ingest{store: s, tx: tx, sourceID: sourceID}
	for _, table := range []string{"epg_channels", "epg_channel_names", "epg_programmes"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE source_id = ?`, sourceID); err != nil {
			item.close()
			return nil, fmt.Errorf("ingest EPG source %q: %w", sourceID, err)
		}
	}
	item.channels, err = tx.Prepare(`INSERT INTO epg_channels(source_id, channel_id, names_json, icons_json, urls_json)
	  VALUES(?, ?, ?, ?, ?) ON CONFLICT(source_id, channel_id) DO UPDATE SET
	  names_json = excluded.names_json, icons_json = excluded.icons_json, urls_json = excluded.urls_json`)
	if err != nil {
		item.close()
		return nil, fmt.Errorf("ingest EPG source %q: %w", sourceID, err)
	}
	item.names, err = tx.Prepare(`INSERT INTO epg_channel_names(source_id, channel_id, normalized) VALUES(?, ?, ?)`)
	if err != nil {
		item.close()
		return nil, fmt.Errorf("ingest EPG source %q: %w", sourceID, err)
	}
	item.programmes, err = tx.Prepare(`INSERT INTO epg_programmes(source_id, channel_id, start_at, start_raw,
	  stop_raw, pdc_raw, vps_raw, showview, videoplus, clumpidx, body) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		item.close()
		return nil, fmt.Errorf("ingest EPG source %q: %w", sourceID, err)
	}
	return item, nil
}

func (i *ingest) addChannel(channel Channel) error {
	names, err := json.Marshal(channel.DisplayNames)
	if err != nil {
		return err
	}
	icons, err := json.Marshal(channel.Icons)
	if err != nil {
		return err
	}
	urls, err := json.Marshal(channel.URLs)
	if err != nil {
		return err
	}
	if _, err := i.channels.Exec(i.sourceID, channel.ID, string(names), string(icons), string(urls)); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(channel.DisplayNames))
	for _, name := range channel.DisplayNames {
		normalized := NormalizeName(name.Value)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		if _, err := i.names.Exec(i.sourceID, channel.ID, normalized); err != nil {
			return err
		}
	}
	i.channelCount++
	return nil
}

func (i *ingest) addProgramme(programme Programme) error {
	_, err := i.programmes.Exec(i.sourceID, programme.Channel, programme.Start.Time.Unix(),
		programme.Start.Raw, optionalRaw(programme.Stop), optionalRaw(programme.PDCStart),
		optionalRaw(programme.VPSStart), programme.ShowView, programme.VideoPlus,
		programme.ClumpIndex, programme.InnerXML)
	if err != nil {
		return err
	}
	i.programmeCount++
	return nil
}

func (i *ingest) commit(digest string, metadata CacheMetadata, updatedAt time.Time) (SourceState, error) {
	_, err := i.tx.Exec(`INSERT INTO epg_sources(source_id, digest, etag, last_modified, content_type,
	  cache_control, fetched_at, updated_at, channel_count, programme_count)
	  VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(source_id) DO UPDATE SET
	  digest = excluded.digest, etag = excluded.etag, last_modified = excluded.last_modified,
	  content_type = excluded.content_type, cache_control = excluded.cache_control,
	  fetched_at = excluded.fetched_at, updated_at = excluded.updated_at,
	  channel_count = excluded.channel_count, programme_count = excluded.programme_count`,
		i.sourceID, digest, metadata.ETag, metadata.LastModified, metadata.ContentType,
		metadata.CacheControl, unixOrZero(metadata.FetchedAt), unixOrZero(updatedAt),
		i.channelCount, i.programmeCount)
	if err != nil {
		i.close()
		return SourceState{}, fmt.Errorf("ingest EPG source %q: %w", i.sourceID, err)
	}
	i.closeStatements()
	if err := i.tx.Commit(); err != nil {
		i.done = true
		return SourceState{}, fmt.Errorf("ingest EPG source %q: %w", i.sourceID, err)
	}
	i.done = true
	i.store.checkpoint()
	return SourceState{
		SourceID: i.sourceID, Digest: digest, Metadata: metadata, UpdatedAt: updatedAt,
		ChannelCount: i.channelCount, ProgrammeCount: i.programmeCount,
	}, nil
}

func (i *ingest) close() {
	if i.done {
		return
	}
	i.done = true
	i.closeStatements()
	_ = i.tx.Rollback()
	i.store.checkpoint()
}

func (i *ingest) closeStatements() {
	for _, statement := range []*sql.Stmt{i.channels, i.names, i.programmes} {
		if statement != nil {
			_ = statement.Close()
		}
	}
	i.channels, i.names, i.programmes = nil, nil, nil
}

func (s *Store) checkpoint() {
	if s.path == "" {
		return
	}
	var busy, log, checkpointed int
	_ = s.sql.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &log, &checkpointed)
	_ = restrictStoreSidecars(s.path)
}

func (s *Store) channelByID(sourceID, channelID string) (storedChannel, bool, error) {
	row := s.sql.QueryRow(`SELECT names_json, icons_json, urls_json FROM epg_channels
	  WHERE source_id = ? AND channel_id = ?`, sourceID, channelID)
	channel := storedChannel{SourceID: sourceID, ChannelID: channelID}
	var names, icons, urls string
	if err := row.Scan(&names, &icons, &urls); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedChannel{}, false, nil
		}
		return storedChannel{}, false, fmt.Errorf("read EPG channel %q: %w", channelID, err)
	}
	if err := decodeChannelPayload(&channel, names, icons, urls); err != nil {
		return storedChannel{}, false, err
	}
	return channel, true, nil
}

func (s *Store) channelsByName(sourceID, normalized string) ([]storedChannel, error) {
	rows, err := s.sql.Query(`SELECT n.channel_id, c.names_json, c.icons_json, c.urls_json
	  FROM epg_channel_names n JOIN epg_channels c
	  ON c.source_id = n.source_id AND c.channel_id = n.channel_id
	  WHERE n.source_id = ? AND n.normalized = ? ORDER BY n.rowid`, sourceID, normalized)
	if err != nil {
		return nil, fmt.Errorf("match EPG channel name %q: %w", normalized, err)
	}
	defer func() { _ = rows.Close() }()
	var channels []storedChannel
	for rows.Next() {
		channel := storedChannel{SourceID: sourceID}
		var names, icons, urls string
		if err := rows.Scan(&channel.ChannelID, &names, &icons, &urls); err != nil {
			return nil, fmt.Errorf("match EPG channel name %q: %w", normalized, err)
		}
		if err := decodeChannelPayload(&channel, names, icons, urls); err != nil {
			return nil, err
		}
		channels = append(channels, channel)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("match EPG channel name %q: %w", normalized, err)
	}
	return channels, nil
}

func decodeChannelPayload(channel *storedChannel, names, icons, urls string) error {
	if err := json.Unmarshal([]byte(names), &channel.DisplayNames); err != nil {
		return fmt.Errorf("decode EPG channel %q names: %w", channel.ChannelID, err)
	}
	if err := json.Unmarshal([]byte(icons), &channel.Icons); err != nil {
		return fmt.Errorf("decode EPG channel %q icons: %w", channel.ChannelID, err)
	}
	if err := json.Unmarshal([]byte(urls), &channel.URLs); err != nil {
		return fmt.Errorf("decode EPG channel %q urls: %w", channel.ChannelID, err)
	}
	return nil
}

func (s *Store) programmes(sourceID, channelID, sourceTimezone string) ([]Programme, error) {
	location, err := loadSourceLocation(sourceTimezone)
	if err != nil {
		return nil, err
	}
	rows, err := s.sql.Query(`SELECT start_raw, stop_raw, pdc_raw, vps_raw, showview, videoplus, clumpidx, body
	  FROM epg_programmes WHERE source_id = ? AND channel_id = ? ORDER BY start_at, rowid`, sourceID, channelID)
	if err != nil {
		return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
	}
	defer func() { _ = rows.Close() }()
	var programmes []Programme
	for rows.Next() {
		var startRaw, stopRaw, pdcRaw, vpsRaw string
		programme := Programme{Channel: channelID}
		if err := rows.Scan(&startRaw, &stopRaw, &pdcRaw, &vpsRaw, &programme.ShowView,
			&programme.VideoPlus, &programme.ClumpIndex, &programme.InnerXML); err != nil {
			return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
		}
		programme.Start, err = parseTimestamp(startRaw, location)
		if err != nil {
			return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
		}
		if programme.Stop, err = parseOptionalTimestamp(stopRaw, location); err != nil {
			return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
		}
		if programme.PDCStart, err = parseOptionalTimestamp(pdcRaw, location); err != nil {
			return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
		}
		if programme.VPSStart, err = parseOptionalTimestamp(vpsRaw, location); err != nil {
			return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
		}
		programmes = append(programmes, programme)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read EPG programmes for %q: %w", channelID, err)
	}
	return programmes, nil
}

func optionalRaw(timestamp *Timestamp) string {
	if timestamp == nil {
		return ""
	}
	return timestamp.Raw
}

func unixOrZero(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}
