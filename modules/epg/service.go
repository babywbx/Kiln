package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
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
	config  ServiceConfig
	fetcher SourceFetcher
	store   CacheStore

	refreshMu sync.Mutex
	gzipMu    sync.Mutex
	mu        sync.RWMutex
	documents map[string]*Document
	versions  map[string]documentVersion
	statuses  map[string]SourceStatus

	outputGeneration uint64
	gzipOutputCache  gzipOutputCache
}

type gzipOutputCache struct {
	generation uint64
	channels   []ChannelRef
	payload    []byte
}

type documentVersion struct {
	digest   [sha256.Size]byte
	timezone string
}

func NewService(config ServiceConfig, fetcher SourceFetcher, store CacheStore) *Service {
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
	return &Service{
		config: config, fetcher: fetcher, store: store,
		documents: make(map[string]*Document), versions: make(map[string]documentVersion),
		statuses: make(map[string]SourceStatus),
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
	s.mu.Lock()
	defer s.mu.Unlock()

	documents := make(map[string]*Document, len(sources))
	versions := make(map[string]documentVersion, len(sources))
	statuses := make(map[string]SourceStatus, len(sources))
	for _, source := range sources {
		if document := s.documents[source.ID]; document != nil {
			documents[source.ID] = document
			if version, ok := s.versions[source.ID]; ok {
				versions[source.ID] = version
			}
		}
		if status, ok := s.statuses[source.ID]; ok {
			statuses[source.ID] = status
		}
	}
	s.config.Sources = sources
	s.documents = documents
	s.versions = versions
	s.statuses = statuses
	s.invalidateOutputCacheLocked()
}

func cloneSources(sources []Source) []Source {
	return append([]Source(nil), sources...)
}

type sourceRefreshResult struct {
	source        Source
	document      *Document
	version       documentVersion
	authoritative bool
	metadata      CacheMetadata
	stale         bool
	err           error
}

type sourceDocumentState struct {
	document *Document
	version  documentVersion
}

func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.mu.RLock()
	states := make(map[string]sourceDocumentState, len(s.config.Sources))
	for _, source := range s.config.Sources {
		states[source.ID] = sourceDocumentState{
			document: s.documents[source.ID],
			version:  s.versions[source.ID],
		}
	}
	s.mu.RUnlock()

	results := make(chan sourceRefreshResult, len(s.config.Sources))
	var wait sync.WaitGroup
	if s.config.MaxRefreshConcurrency <= 0 || s.config.MaxRefreshConcurrency >= len(s.config.Sources) {
		for _, source := range s.config.Sources {
			source := source
			state := states[source.ID]
			wait.Add(1)
			go func() {
				defer wait.Done()
				results <- s.refreshSource(ctx, source, state)
			}()
		}
	} else {
		refreshSlots := make(chan struct{}, s.config.MaxRefreshConcurrency)
		for _, source := range s.config.Sources {
			source := source
			state := states[source.ID]
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
	bySource := make(map[string]sourceRefreshResult, len(s.config.Sources))
	var refreshErrors []error
	for result := range results {
		bySource[result.source.ID] = result
		if result.err != nil {
			refreshErrors = append(refreshErrors, fmt.Errorf("refresh EPG source %q: %w", result.source.ID, result.err))
		}
	}

	s.mu.Lock()
	documentsChanged := false
	for _, source := range s.config.Sources {
		result := bySource[source.ID]
		status := s.statuses[source.ID]
		status.SourceID = source.ID
		status.LastAttempt = now
		if result.document != nil {
			current := s.documents[source.ID]
			currentVersion, versioned := s.versions[source.ID]
			versionChanged := !versioned || currentVersion != result.version
			if current == nil || result.authoritative && versionChanged {
				s.documents[source.ID] = result.document
				s.versions[source.ID] = result.version
				documentsChanged = true
			}
		}
		if result.metadata != (CacheMetadata{}) {
			status.Metadata = result.metadata
		}
		if result.err != nil {
			status.Stale = result.stale || (result.document == nil && s.documents[source.ID] != nil)
			status.Error = result.err.Error()
		} else {
			status.Stale = false
			status.Error = ""
			status.LastSuccess = now
		}
		if document := s.documents[source.ID]; document != nil {
			status.Available = true
			status.ChannelCount = len(document.Channels)
			status.ProgrammeCount = len(document.Programmes)
		}
		s.statuses[source.ID] = status
	}
	if documentsChanged {
		s.invalidateOutputCacheLocked()
	}
	s.mu.Unlock()
	return errors.Join(refreshErrors...)
}

func (s *Service) refreshSource(ctx context.Context, source Source, state sourceDocumentState) sourceRefreshResult {
	result := sourceRefreshResult{source: source}
	var cached CacheEntry
	var found bool
	var cacheLoadError error
	if s.store != nil {
		cached, found, cacheLoadError = loadCacheEntry(s.store, source.ID)
		if found && int64(len(cached.Data)) > s.config.MaxSourceBytes {
			cacheLoadError = errors.Join(cacheLoadError, fmt.Errorf("cached XMLTV: %w (%d bytes)", ErrSourceTooLarge, s.config.MaxSourceBytes))
			cached = CacheEntry{}
			found = false
		}
		if found {
			result.metadata = cached.Metadata
		}
	}

	fetched, fetchError := s.fetcher.Fetch(ctx, source, cached.Metadata)
	if fetchError != nil {
		result.err = errors.Join(cacheLoadError, fetchError)
		if state.document != nil {
			result.document = state.document
			result.version = state.version
			result.stale = true
			return result
		}
		if found {
			timezone := s.timezoneFor(source)
			result.document, result.err = parseFallback(source, timezone, cached.Data, result.err)
			result.version = versionDocument(cached.Data, timezone)
			result.stale = result.document != nil
		}
		return result
	}
	if !fetched.NotModified && int64(len(fetched.Data)) > s.config.MaxSourceBytes {
		result.err = errors.Join(cacheLoadError, fmt.Errorf("downloaded XMLTV: %w (%d bytes)", ErrSourceTooLarge, s.config.MaxSourceBytes))
		if state.document != nil {
			result.document = state.document
			result.version = state.version
			result.stale = true
			return result
		}
		if found {
			timezone := s.timezoneFor(source)
			result.document, result.err = parseFallback(source, timezone, cached.Data, result.err)
			result.version = versionDocument(cached.Data, timezone)
			result.stale = result.document != nil
		}
		return result
	}
	if fetched.NotModified {
		result.metadata = fetched.Metadata
		if !found {
			result.err = errors.Join(cacheLoadError, fmt.Errorf("source returned 304 without a cached body"))
			return result
		}
		timezone := s.timezoneFor(source)
		version := versionDocument(cached.Data, timezone)
		if state.document != nil && state.version == version {
			result.document = state.document
			result.version = state.version
			result.authoritative = true
			return result
		}
		document, err := ParseBytes(cached.Data, timezone)
		if err != nil {
			result.err = errors.Join(cacheLoadError, fmt.Errorf("parse cached XMLTV: %w", err))
			return result
		}
		result.document = document
		result.version = version
		result.authoritative = true
		return result
	}

	timezone := s.timezoneFor(source)
	version := versionDocument(fetched.Data, timezone)
	if state.document != nil && state.version == version {
		result.document = state.document
	} else {
		document, parseError := ParseBytes(fetched.Data, timezone)
		if parseError != nil {
			result.err = errors.Join(cacheLoadError, fmt.Errorf("parse downloaded XMLTV: %w", parseError))
			if state.document != nil {
				result.document = state.document
				result.version = state.version
				result.stale = true
				return result
			}
			if found {
				result.document, result.err = parseFallback(source, timezone, cached.Data, result.err)
				result.version = versionDocument(cached.Data, timezone)
				result.stale = result.document != nil
			}
			return result
		}
		result.document = document
	}
	result.version = version
	result.authoritative = true
	result.metadata = fetched.Metadata
	if s.store != nil {
		updatedAt := fetched.Metadata.FetchedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if err := s.store.Save(CacheEntry{
			SourceID: source.ID, Data: fetched.Data,
			Metadata: fetched.Metadata, UpdatedAt: updatedAt,
		}); err != nil {
			result.err = errors.Join(cacheLoadError, fmt.Errorf("save cache: %w", err))
			return result
		}
	}
	result.err = cacheLoadError
	return result
}

func versionDocument(data []byte, timezone string) documentVersion {
	return documentVersion{digest: sha256.Sum256(data), timezone: timezone}
}

func parseFallback(source Source, timezone string, data []byte, prior error) (*Document, error) {
	document, err := ParseBytes(data, timezone)
	if err != nil {
		return nil, errors.Join(prior, fmt.Errorf("parse cached XMLTV for %q: %w", source.ID, err))
	}
	return document, prior
}

func (s *Service) timezoneFor(source Source) string {
	if source.Timezone != "" {
		return source.Timezone
	}
	return s.config.DefaultTimezone
}

func (s *Service) Run(ctx context.Context) {
	s.reportRefreshError(s.Refresh(ctx))
	ticker := time.NewTicker(s.config.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reportRefreshError(s.Refresh(ctx))
		}
	}
}

func (s *Service) Start(ctx context.Context) {
	go s.Run(ctx)
}

func (s *Service) reportRefreshError(err error) {
	if err != nil && s.config.OnError != nil {
		s.config.OnError(err)
	}
}

func (s *Service) Snapshot() []SourceDocument {
	s.mu.RLock()
	defer s.mu.RUnlock()
	documents := make([]SourceDocument, 0, len(s.config.Sources))
	for _, source := range s.config.Sources {
		if document := s.documents[source.ID]; document != nil {
			documents = append(documents, SourceDocument{Source: source, Document: document})
		}
	}
	return documents
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
	documents := s.Snapshot()
	results := make([]MatchResult, 0, len(channels))
	for _, channel := range channels {
		results = append(results, MatchChannel(channel, documents))
	}
	return results
}

func (s *Service) Document(channels []ChannelRef) *Document {
	documents := s.Snapshot()
	output := &Document{GeneratorInfoName: s.config.GeneratorInfoName}
	seenKilnIDs := make(map[string]struct{}, len(channels))
	for _, channel := range channels {
		if channel.ID == "" {
			continue
		}
		if _, exists := seenKilnIDs[channel.ID]; exists {
			continue
		}
		match := MatchChannel(channel, documents)
		if match.Status != MatchMatched || match.Match == nil {
			continue
		}
		sourceDocument, sourceChannel, ok := findMatchedChannel(documents, *match.Match)
		if !ok {
			continue
		}
		seenKilnIDs[channel.ID] = struct{}{}
		rewritten := sourceChannel
		rewritten.ID = channel.ID
		rewritten.DisplayNames = append([]Text(nil), sourceChannel.DisplayNames...)
		rewritten.URLs = append([]string(nil), sourceChannel.URLs...)
		rewritten.InnerXML = ""
		rewritten.Icons = outputIcons(channel, sourceChannel)
		output.Channels = append(output.Channels, rewritten)
		for _, programme := range sourceDocument.Document.Programmes {
			if programme.Channel != sourceChannel.ID {
				continue
			}
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

func findMatchedChannel(documents []SourceDocument, match MatchCandidate) (SourceDocument, Channel, bool) {
	for _, document := range documents {
		if document.Source.ID != match.SourceID || document.Document == nil {
			continue
		}
		for _, channel := range document.Document.Channels {
			if channel.ID == match.ChannelID {
				return document, channel, true
			}
		}
	}
	return SourceDocument{}, Channel{}, false
}

func outputIcons(channel ChannelRef, source Channel) []Icon {
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
