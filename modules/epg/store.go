package epg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const cacheFormatVersion = 1

type CacheEntry struct {
	SourceID  string        `json:"source_id"`
	Data      []byte        `json:"-"`
	Metadata  CacheMetadata `json:"metadata"`
	UpdatedAt time.Time     `json:"updated_at"`
}

type CacheStore interface {
	Load(sourceID string) (CacheEntry, bool, error)
	Save(CacheEntry) error
}

type cacheHeader struct {
	Version   int           `json:"version"`
	SourceID  string        `json:"source_id"`
	Metadata  CacheMetadata `json:"metadata"`
	UpdatedAt time.Time     `json:"updated_at"`
	Size      int64         `json:"size"`
	SHA256    string        `json:"sha256"`
}

type DiskStore struct {
	directory string
	mu        sync.Mutex
	entries   map[string]CacheEntry
}

func NewDiskStore(directory string) (*DiskStore, error) {
	if directory == "" {
		return nil, fmt.Errorf("EPG cache directory is empty")
	}
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return nil, fmt.Errorf("create EPG cache directory: %w", err)
	}
	return &DiskStore{directory: directory, entries: make(map[string]CacheEntry)}, nil
}

func (s *DiskStore) Load(sourceID string) (CacheEntry, bool, error) {
	entry, found, err := s.loadImmutable(sourceID)
	return cloneCacheEntry(entry), found, err
}

// Returned data is shared; callers must not mutate it.
func (s *DiskStore) loadImmutable(sourceID string) (CacheEntry, bool, error) {
	if sourceID == "" {
		return CacheEntry{}, false, fmt.Errorf("EPG cache source ID is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, ok := s.entries[sourceID]; ok {
		return entry, true, nil
	}
	payload, err := os.ReadFile(s.path(sourceID))
	if os.IsNotExist(err) {
		return CacheEntry{}, false, nil
	}
	if err != nil {
		return CacheEntry{}, false, fmt.Errorf("read EPG cache %q: %w", sourceID, err)
	}
	newline := bytes.IndexByte(payload, '\n')
	if newline < 0 {
		return CacheEntry{}, false, fmt.Errorf("read EPG cache %q: missing header", sourceID)
	}
	var header cacheHeader
	if err := json.Unmarshal(payload[:newline], &header); err != nil {
		return CacheEntry{}, false, fmt.Errorf("read EPG cache %q header: %w", sourceID, err)
	}
	data := payload[newline+1:]
	if header.Version != cacheFormatVersion || header.SourceID != sourceID {
		return CacheEntry{}, false, fmt.Errorf("read EPG cache %q: header mismatch", sourceID)
	}
	if header.Size != int64(len(data)) {
		return CacheEntry{}, false, fmt.Errorf("read EPG cache %q: size mismatch", sourceID)
	}
	digest := sha256.Sum256(data)
	if header.SHA256 != hex.EncodeToString(digest[:]) {
		return CacheEntry{}, false, fmt.Errorf("read EPG cache %q: checksum mismatch", sourceID)
	}
	entry := CacheEntry{
		SourceID: header.SourceID, Data: data,
		Metadata: header.Metadata, UpdatedAt: header.UpdatedAt,
	}
	s.entries[sourceID] = entry
	return entry, true, nil
}

func (s *DiskStore) Save(entry CacheEntry) error {
	if entry.SourceID == "" {
		return fmt.Errorf("EPG cache source ID is empty")
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}
	entry = cloneCacheEntry(entry)
	digest := sha256.Sum256(entry.Data)
	header := cacheHeader{
		Version: cacheFormatVersion, SourceID: entry.SourceID,
		Metadata: entry.Metadata, UpdatedAt: entry.UpdatedAt,
		Size: int64(len(entry.Data)), SHA256: hex.EncodeToString(digest[:]),
	}
	headerData, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("encode EPG cache %q header: %w", entry.SourceID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	temporary, err := os.CreateTemp(s.directory, ".epg-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary EPG cache %q: %w", entry.SourceID, err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set EPG cache %q permissions: %w", entry.SourceID, err)
	}
	if _, err := temporary.Write(headerData); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write EPG cache %q header: %w", entry.SourceID, err)
	}
	if _, err := temporary.Write([]byte{'\n'}); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write EPG cache %q header delimiter: %w", entry.SourceID, err)
	}
	if _, err := temporary.Write(entry.Data); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write EPG cache %q: %w", entry.SourceID, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync EPG cache %q: %w", entry.SourceID, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close EPG cache %q: %w", entry.SourceID, err)
	}
	if err := os.Rename(temporaryName, s.path(entry.SourceID)); err != nil {
		return fmt.Errorf("replace EPG cache %q: %w", entry.SourceID, err)
	}
	if directory, err := os.Open(s.directory); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	s.entries[entry.SourceID] = entry
	return nil
}

func (s *DiskStore) path(sourceID string) string {
	digest := sha256.Sum256([]byte(sourceID))
	return filepath.Join(s.directory, hex.EncodeToString(digest[:])+".cache")
}

type MemoryStore struct {
	mu      sync.RWMutex
	entries map[string]CacheEntry
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{entries: make(map[string]CacheEntry)}
}

func (s *MemoryStore) Load(sourceID string) (CacheEntry, bool, error) {
	entry, found, err := s.loadImmutable(sourceID)
	return cloneCacheEntry(entry), found, err
}

// Returned data is shared; callers must not mutate it.
func (s *MemoryStore) loadImmutable(sourceID string) (CacheEntry, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.entries[sourceID]
	return entry, ok, nil
}

func (s *MemoryStore) Save(entry CacheEntry) error {
	if entry.SourceID == "" {
		return fmt.Errorf("EPG cache source ID is empty")
	}
	if entry.UpdatedAt.IsZero() {
		entry.UpdatedAt = time.Now().UTC()
	}
	s.mu.Lock()
	s.entries[entry.SourceID] = cloneCacheEntry(entry)
	s.mu.Unlock()
	return nil
}

func cloneCacheEntry(entry CacheEntry) CacheEntry {
	entry.Data = append([]byte(nil), entry.Data...)
	return entry
}

type immutableCacheStore interface {
	loadImmutable(sourceID string) (CacheEntry, bool, error)
}

func loadCacheEntry(store CacheStore, sourceID string) (CacheEntry, bool, error) {
	if store, ok := store.(immutableCacheStore); ok {
		return store.loadImmutable(sourceID)
	}
	return store.Load(sourceID)
}
