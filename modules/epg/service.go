package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"
)

const DefaultRefreshInterval = 6 * time.Hour

const maxGzipOutputCacheBytes = 8 << 20

type ServiceConfig struct {
	Sources               []Source
	DefaultTimezone       string
	RefreshInterval       time.Duration
	GeneratorInfoName     string
	MaxRefreshConcurrency int
	MaxSourceBytes        int64
	OnError               func(error)
}

type SourceStatus struct {
	SourceID       string        `json:"source_id"`
	LastAttempt    time.Time     `json:"last_attempt,omitempty"`
	LastSuccess    time.Time     `json:"last_success,omitempty"`
	Stale          bool          `json:"stale"`
	Error          string        `json:"error,omitempty"`
	ChannelCount   int           `json:"channel_count"`
	ProgrammeCount int           `json:"programme_count"`
	Available      bool          `json:"available"`
	Metadata       CacheMetadata `json:"metadata"`
}

type Service struct {
	config   ServiceConfig
	fetcher  SourceFetcher
	store    *Store
	storeErr error

	refreshMu sync.Mutex
	gzipMu    sync.Mutex
	mu        sync.RWMutex
	statuses  map[string]SourceStatus
	digests   map[string][sha256.Size]byte

	outputGeneration uint64
	gzipOutputCache  gzipOutputCache
}

type gzipOutputCache struct {
	generation uint64
	channels   []ChannelRef
	payload    []byte
}

func NewService(config ServiceConfig, fetcher SourceFetcher, store *Store) *Service {
	config.Sources = cloneSources(config.Sources)
	if config.DefaultTimezone == "" {
		config.DefaultTimezone = DefaultTimezone
	}
	if config.RefreshInterval <= 0 {
		config.RefreshInterval = DefaultRefreshInterval
	}
	if config.GeneratorInfoName == "" {
		config.GeneratorInfoName = "Kiln"
	}
	if config.MaxSourceBytes <= 0 {
		config.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if fetcher == nil {
		fetcher = &Fetcher{}
	}
	service := &Service{
		config: config, fetcher: fetcher, store: store,
		statuses: make(map[string]SourceStatus), digests: make(map[string][sha256.Size]byte),
	}
	if store == nil {
		service.store, service.storeErr = NewMemoryStore()
	}
	service.loadPersistedState()
	return service
}

func (s *Service) Close() error {
	if s.store == nil {
		return nil
	}
	return s.store.Close()
}

func (s *Service) loadPersistedState() {
	if s.store == nil {
		return
	}
	states, err := s.store.states()
	if err != nil {
		s.reportError(err)
		return
	}
	for _, source := range s.config.Sources {
		state, ok := states[source.ID]
		if !ok {
			continue
		}
		digest, ok := decodeDigest(state.Digest)
		if ok {
			s.digests[source.ID] = digest
		}
		s.statuses[source.ID] = SourceStatus{
			SourceID: source.ID, Metadata: state.Metadata,
			ChannelCount: state.ChannelCount, ProgrammeCount: state.ProgrammeCount,
			Available: ok,
		}
	}
}

func (s *Service) Sources() []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSources(s.config.Sources)
}

func (s *Service) SetSources(sources []Source) {
	sources = cloneSources(sources)
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.store != nil {
		s.store.mu.Lock()
		defer s.store.mu.Unlock()
	}
	s.mu.Lock()
	statuses := make(map[string]SourceStatus, len(sources))
	digests := make(map[string][sha256.Size]byte, len(sources))
	retained := make([]string, 0, len(sources))
	for _, source := range sources {
		retained = append(retained, source.ID)
		if status, ok := s.statuses[source.ID]; ok {
			statuses[source.ID] = status
		}
		if digest, ok := s.digests[source.ID]; ok {
			digests[source.ID] = digest
		}
	}
	s.config.Sources = sources
	s.statuses = statuses
	s.digests = digests
	s.invalidateOutputCacheLocked()
	s.mu.Unlock()
	if s.store != nil {
		if err := s.store.retain(retained); err != nil {
			s.reportError(err)
		}
	}
}

func cloneSources(sources []Source) []Source {
	return append([]Source(nil), sources...)
}

type knownState struct {
	digest    [sha256.Size]byte
	metadata  CacheMetadata
	available bool
}

type sourceRefreshResult struct {
	source      Source
	metadata    CacheMetadata
	state       SourceState
	digest      [sha256.Size]byte
	metadataSet bool
	ingested    bool
	stale       bool
	err         error
}

func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if s.store == nil {
		return s.storeErr
	}

	s.mu.RLock()
	sources := s.config.Sources
	known := make(map[string]knownState, len(sources))
	for _, source := range sources {
		status := s.statuses[source.ID]
		known[source.ID] = knownState{
			digest: s.digests[source.ID], metadata: status.Metadata, available: status.Available,
		}
	}
	s.mu.RUnlock()

	results := make(chan sourceRefreshResult, len(sources))
	var wait sync.WaitGroup
	if s.config.MaxRefreshConcurrency <= 0 || s.config.MaxRefreshConcurrency >= len(sources) {
		for _, source := range sources {
			state := known[source.ID]
			wait.Add(1)
			go func() {
				defer wait.Done()
				results <- s.refreshSource(ctx, source, state)
			}()
		}
	} else {
		refreshSlots := make(chan struct{}, s.config.MaxRefreshConcurrency)
		for _, source := range sources {
			state := known[source.ID]
			refreshSlots <- struct{}{}
			wait.Add(1)
			go func() {
				defer wait.Done()
				defer func() { <-refreshSlots }()
				results <- s.refreshSource(ctx, source, state)
			}()
		}
	}
	wait.Wait()
	close(results)

	now := time.Now().UTC()
	bySource := make(map[string]sourceRefreshResult, len(sources))
	var refreshErrors []error
	for result := range results {
		bySource[result.source.ID] = result
		if result.err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("refresh EPG source %q: %w", result.source.ID, result.err))
		}
	}

	s.mu.Lock()
	for _, source := range s.config.Sources {
		result, ok := bySource[source.ID]
		if !ok {
			continue
		}
		status := s.statuses[source.ID]
		status.SourceID = source.ID
		status.LastAttempt = now
		if result.metadataSet {
			status.Metadata = result.metadata
		}
		if result.ingested {
			status.ChannelCount = result.state.ChannelCount
			status.ProgrammeCount = result.state.ProgrammeCount
			status.Available = true
			s.digests[source.ID] = result.digest
		}
		if result.err != nil {
			status.Stale = result.stale
			status.Error = result.err.Error()
		} else {
			status.Stale = false
			status.Error = ""
			status.LastSuccess = now
		}
		s.statuses[source.ID] = status
	}
	s.mu.Unlock()
	return errors.Join(refreshErrors...)
}

func (s *Service) refreshSource(ctx context.Context, source Source, known knownState) sourceRefreshResult {
	result := sourceRefreshResult{source: source, digest: known.digest}
	fetched, err := s.fetcher.Fetch(ctx, source, known.metadata)
	if err != nil {
		result.err = err
		result.stale = known.available
		return result
	}
	defer func() { _ = fetched.Close() }()

	if fetched.NotModified {
		if !known.available {
			result.err = fmt.Errorf("source returned 304 without cached data")
			return result
		}
		metadata := mergeMetadata(known.metadata, fetched.Metadata)
		if err := s.persistMetadata(source.ID, known.metadata, metadata); err != nil {
			result.err = err
			return result
		}
		result.metadata, result.metadataSet = metadata, true
		return result
	}

	var precomputed [sha256.Size]byte
	if fetched.Body == nil {
		precomputed = sha256.Sum256(fetched.Data)
		if precomputed == known.digest {
			if err := s.persistMetadata(source.ID, known.metadata, fetched.Metadata); err != nil {
				result.err = err
				return result
			}
			result.metadata, result.metadataSet = fetched.Metadata, true
			return result
		}
	}
	state, digest, ingested, err := s.ingestSource(source, known, fetched, precomputed)
	if err != nil {
		result.err = err
		result.stale = known.available
		return result
	}
	result.state, result.digest, result.ingested = state, digest, ingested
	if !ingested {
		if err := s.persistMetadata(source.ID, known.metadata, fetched.Metadata); err != nil {
			result.err = err
			return result
		}
	}
	result.metadata, result.metadataSet = fetched.Metadata, true
	return result
}

func (s *Service) persistMetadata(sourceID string, previous, current CacheMetadata) error {
	if current == previous {
		return nil
	}
	return s.store.saveMetadata(sourceID, current)
}

func (s *Service) ingestSource(source Source, known knownState, fetched FetchResult,
	precomputed [sha256.Size]byte) (SourceState, [sha256.Size]byte, bool, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	item, err := s.store.beginIngest(source.ID)
	if err != nil {
		return SourceState{}, known.digest, false, err
	}
	defer item.close()

	limited := &sourceBody{reader: fetched.reader(), sourceID: source.ID, limit: s.config.MaxSourceBytes}
	var reader io.Reader = limited
	hasher := sha256.New()
	streaming := fetched.Body != nil
	if streaming {
		reader = io.TeeReader(limited, hasher)
	}
	scanErr := Scan(reader, s.timezoneFor(source), Handler{
		Channel:   item.addChannel,
		Programme: item.addProgramme,
	})
	if limited.err != nil {
		return SourceState{}, known.digest, false, limited.err
	}
	if scanErr != nil {
		return SourceState{}, known.digest, false, fmt.Errorf("parse XMLTV: %w", scanErr)
	}
	digest := precomputed
	if streaming {
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return SourceState{}, known.digest, false, err
		}
		if limited.err != nil {
			return SourceState{}, known.digest, false, limited.err
		}
		copy(digest[:], hasher.Sum(nil))
	}
	if digest == known.digest {
		return SourceState{}, known.digest, false, nil
	}
	updatedAt := fetched.Metadata.FetchedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	state, err := item.commit(encodeDigest(digest), fetched.Metadata, updatedAt)
	if err != nil {
		return SourceState{}, known.digest, false, err
	}
	s.mu.Lock()
	s.invalidateOutputCacheLocked()
	s.mu.Unlock()
	return state, digest, true, nil
}

func encodeDigest(digest [sha256.Size]byte) string {
	return ingestVersion + ":" + hex.EncodeToString(digest[:])
}

func decodeDigest(value string) ([sha256.Size]byte, bool) {
	var digest [sha256.Size]byte
	prefix := ingestVersion + ":"
	if len(value) != len(prefix)+2*sha256.Size || value[:len(prefix)] != prefix {
		return digest, false
	}
	raw, err := hex.DecodeString(value[len(prefix):])
	if err != nil {
		return digest, false
	}
	copy(digest[:], raw)
	return digest, true
}

func (s *Service) timezoneFor(source Source) string {
	if source.Timezone != "" {
		return source.Timezone
	}
	return s.config.DefaultTimezone
}

func (s *Service) timezoneForID(sourceID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, source := range s.config.Sources {
		if source.ID == sourceID {
			return s.timezoneFor(source)
		}
	}
	return s.config.DefaultTimezone
}

func (s *Service) Run(ctx context.Context) {
	s.reportError(s.Refresh(ctx))
	ticker := time.NewTicker(s.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reportError(s.Refresh(ctx))
		}
	}
}

func (s *Service) Start(ctx context.Context) {
	go s.Run(ctx)
}

func (s *Service) reportError(err error) {
	if err != nil && s.config.OnError != nil {
		s.config.OnError(err)
	}
}

func (s *Service) Statuses() []SourceStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	statuses := make([]SourceStatus, 0, len(s.config.Sources))
	for _, source := range s.config.Sources {
		status := s.statuses[source.ID]
		status.SourceID = source.ID
		statuses = append(statuses, status)
	}
	return statuses
}

func (s *Service) Matches(channels []ChannelRef) []MatchResult {
	var readErrors []error
	defer func() {
		for _, err := range readErrors {
			s.reportError(err)
		}
	}()
	if s.store != nil {
		s.store.mu.RLock()
		defer s.store.mu.RUnlock()
	}
	results := make([]MatchResult, 0, len(channels))
	for _, channel := range channels {
		result, _, err := s.matchChannel(channel)
		if err != nil {
			readErrors = append(readErrors, err)
		}
		results = append(results, result)
	}
	return results
}

func (s *Service) Document(channels []ChannelRef) *Document {
	output := &Document{GeneratorInfoName: s.config.GeneratorInfoName}
	if s.store == nil {
		return output
	}
	s.store.mu.RLock()
	var readErrors []error
	defer func() {
		s.store.mu.RUnlock()
		for _, err := range readErrors {
			s.reportError(err)
		}
	}()
	seenKilnIDs := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if channel.ID == "" {
			continue
		}
		if _, exists := seenKilnIDs[channel.ID]; exists {
			continue
		}
		match, matched, err := s.matchChannel(channel)
		if err != nil {
			readErrors = append(readErrors, err)
			continue
		}
		if match.Status != MatchMatched || len(matched) == 0 {
			continue
		}
		source := matched[0]
		programmes, err := s.store.programmes(source.SourceID, source.ChannelID, s.timezoneForID(source.SourceID))
		if err != nil {
			readErrors = append(readErrors, err)
			continue
		}
		seenKilnIDs[channel.ID] = struct{}{}
		output.Channels = append(output.Channels, Channel{
			ID: channel.ID, DisplayNames: source.DisplayNames,
			URLs: source.URLs, Icons: outputIcons(channel, source),
		})
		for _, programme := range programmes {
			programme.Channel = channel.ID
			output.Programmes = append(output.Programmes, programme)
		}
	}
	return output
}

func (s *Service) XML(channels []ChannelRef) ([]byte, error) {
	return Marshal(s.Document(channels))
}

func (s *Service) GzipXML(channels []ChannelRef) ([]byte, error) {
	if payload, _, ok := s.cachedGzipOutput(channels); ok {
		return payload, nil
	}

	s.gzipMu.Lock()
	defer s.gzipMu.Unlock()
	cachedPayload, generation, ok := s.cachedGzipOutput(channels)
	if ok {
		return cachedPayload, nil
	}

	payload, err := s.XML(channels)
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(payload); err != nil {
		return nil, fmt.Errorf("compress XMLTV: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("compress XMLTV: %w", err)
	}
	compressedPayload := compressed.Bytes()
	if len(compressedPayload) > gzipOutputCacheLimit(s.config.MaxSourceBytes) {
		return compressedPayload, nil
	}

	cached := gzipOutputCache{
		generation: generation,
		channels:   append([]ChannelRef(nil), channels...),
		payload:    bytes.Clone(compressedPayload),
	}
	s.mu.Lock()
	if s.outputGeneration == generation {
		s.gzipOutputCache = cached
	}
	s.mu.Unlock()
	return compressedPayload, nil
}

func (s *Service) cachedGzipOutput(channels []ChannelRef) ([]byte, uint64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	generation := s.outputGeneration
	if s.gzipOutputCache.payload != nil &&
		s.gzipOutputCache.generation == generation &&
		slices.Equal(s.gzipOutputCache.channels, channels) {
		return bytes.Clone(s.gzipOutputCache.payload), generation, true
	}
	return nil, generation, false
}

func (s *Service) invalidateOutputCacheLocked() {
	s.outputGeneration++
	s.gzipOutputCache = gzipOutputCache{}
}

func gzipOutputCacheLimit(maxSourceBytes int64) int {
	limit := maxSourceBytes / 4
	if limit <= 0 {
		return 0
	}
	if limit > maxGzipOutputCacheBytes {
		return maxGzipOutputCacheBytes
	}
	return int(limit)
}

func outputIcons(channel ChannelRef, source storedChannel) []Icon {
	if channel.LogoURL != "" {
		return []Icon{{Src: channel.LogoURL}}
	}
	name := firstNonEmpty(channel.EPGName, channel.Title)
	if name == "" && len(source.DisplayNames) > 0 {
		name = source.DisplayNames[0].Value
	}
	if candidates := LogoCandidates(name); len(candidates) > 0 {
		return []Icon{{Src: candidates[0].URL}}
	}
	icons := make([]Icon, 0, len(source.Icons))
	for _, icon := range source.Icons {
		if icon.Src != "" {
			icons = append(icons, icon)
		}
	}
	return icons
}
