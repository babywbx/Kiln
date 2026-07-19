package epg

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const DefaultRefreshInterval = 6 * time.Hour

type ServiceConfig struct {
	Sources           []Source
	DefaultTimezone   string
	RefreshInterval   time.Duration
	GeneratorInfoName string
	// MaxRefreshConcurrency limits simultaneous source fetch and parse work.
	// Zero or less preserves the default of refreshing every source in parallel.
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
	mu        sync.RWMutex
	documents map[string]*Document
	statuses  map[string]SourceStatus
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
		documents: make(map[string]*Document), statuses: make(map[string]SourceStatus),
	}
}

// Sources returns an independent copy of the active source configuration.
func (s *Service) Sources() []Source {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneSources(s.config.Sources)
}

// SetSources atomically replaces the active source list. It is serialized with
// Refresh, retains snapshots and status for IDs that remain configured, and
// immediately removes state for deleted IDs.
func (s *Service) SetSources(sources []Source) {
	sources = cloneSources(sources)
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()

	documents := make(map[string]*Document, len(sources))
	statuses := make(map[string]SourceStatus, len(sources))
	for _, source := range sources {
		if document := s.documents[source.ID]; document != nil {
			documents[source.ID] = document
		}
		if status, ok := s.statuses[source.ID]; ok {
			statuses[source.ID] = status
		}
	}
	s.config.Sources = sources
	s.documents = documents
	s.statuses = statuses
}

func cloneSources(sources []Source) []Source {
	return append([]Source(nil), sources...)
}

type sourceRefreshResult struct {
	source   Source
	document *Document
	metadata CacheMetadata
	stale    bool
	err      error
}

// Refresh updates sources concurrently and publishes a complete new snapshot
// atomically. Overlapping Refresh calls are serialized.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	results := make(chan sourceRefreshResult, len(s.config.Sources))
	var wait sync.WaitGroup
	if s.config.MaxRefreshConcurrency <= 0 || s.config.MaxRefreshConcurrency >= len(s.config.Sources) {
		for _, source := range s.config.Sources {
			source := source
			wait.Add(1)
			go func() {
				defer wait.Done()
				results <- s.refreshSource(ctx, source)
			}()
		}
	} else {
		refreshSlots := make(chan struct{}, s.config.MaxRefreshConcurrency)
		for _, source := range s.config.Sources {
			source := source
			refreshSlots <- struct{}{}
			wait.Add(1)
			go func() {
				defer wait.Done()
				defer func() { <-refreshSlots }()
				results <- s.refreshSource(ctx, source)
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
	for _, source := range s.config.Sources {
		result := bySource[source.ID]
		status := s.statuses[source.ID]
		status.SourceID = source.ID
		status.LastAttempt = now
		if result.document != nil {
			s.documents[source.ID] = result.document
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
	s.mu.Unlock()
	return errors.Join(refreshErrors...)
}

func (s *Service) refreshSource(ctx context.Context, source Source) sourceRefreshResult {
	result := sourceRefreshResult{source: source}
	var cached CacheEntry
	var found bool
	var cacheLoadError error
	if s.store != nil {
		cached, found, cacheLoadError = s.store.Load(source.ID)
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
		if found {
			result.document, result.err = parseFallback(source, s.timezoneFor(source), cached.Data, result.err)
			result.stale = result.document != nil
		}
		return result
	}
	if !fetched.NotModified && int64(len(fetched.Data)) > s.config.MaxSourceBytes {
		result.err = errors.Join(cacheLoadError, fmt.Errorf("downloaded XMLTV: %w (%d bytes)", ErrSourceTooLarge, s.config.MaxSourceBytes))
		if found {
			result.document, result.err = parseFallback(source, s.timezoneFor(source), cached.Data, result.err)
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
		document, err := ParseBytes(cached.Data, s.timezoneFor(source))
		if err != nil {
			result.err = errors.Join(cacheLoadError, fmt.Errorf("parse cached XMLTV: %w", err))
			return result
		}
		result.document = document
		return result
	}

	document, parseError := ParseBytes(fetched.Data, s.timezoneFor(source))
	if parseError != nil {
		result.err = errors.Join(cacheLoadError, fmt.Errorf("parse downloaded XMLTV: %w", parseError))
		if found {
			result.document, result.err = parseFallback(source, s.timezoneFor(source), cached.Data, result.err)
			result.stale = result.document != nil
		}
		return result
	}
	result.document = document
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

// Run refreshes immediately and then at the configured interval until ctx is
// cancelled. Refresh failures are reported through OnError while stale data
// remains available.
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

// Snapshot returns the currently usable source documents in configured order.
// Documents are immutable after publication.
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

// Document filters the current snapshot to explicitly matched Kiln channels
// and rewrites every XMLTV channel reference to the Kiln channel ID.
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
	return compressed.Bytes(), nil
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
