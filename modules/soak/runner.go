package soak

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultDuration             = 24 * time.Hour
	defaultInterval             = 10 * time.Second
	defaultStallTimeout         = 2 * time.Minute
	defaultRequestTimeout       = 15 * time.Second
	defaultConcurrency          = 4
	defaultMaxConsecutiveErrors = 3
	maxResponseBytes            = 16 << 20
	maxAssetBytes               = 256 << 20
)

var (
	ErrStalled           = errors.New("media playlist stalled")
	ErrSequenceRegressed = errors.New("media sequence regressed")
	ErrTooManyErrors     = errors.New("consecutive HTTP error threshold reached")
	errNoMediaAssets     = errors.New("playlist has no complete segment or part")
)

type Config struct {
	BaseURL              string
	Token                string
	Username             string
	Password             string
	Channels             []string
	Concurrency          int
	Duration             time.Duration
	Interval             time.Duration
	StallTimeout         time.Duration
	RequestTimeout       time.Duration
	MaxConsecutiveErrors int
	StatusPath           string
	MetricsPath          string
}

type Option func(*Runner)

func WithOutput(w io.Writer) Option {
	return func(r *Runner) {
		if w != nil {
			r.output = w
		}
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(r *Runner) {
		if client != nil {
			r.client = client
		}
	}
}

type Runner struct {
	cfg     Config
	baseURL *url.URL
	client  *http.Client
	output  io.Writer
	writeMu sync.Mutex
}

type ChannelReport struct {
	ID                  string    `json:"id"`
	PlaylistRequests    uint64    `json:"playlist_requests"`
	SegmentRequests     uint64    `json:"segment_requests"`
	Bytes               uint64    `json:"bytes"`
	HTTPErrors          uint64    `json:"http_errors"`
	ProgressEvents      uint64    `json:"progress_events"`
	Discontinuities     uint64    `json:"discontinuities"`
	Stalls              uint64    `json:"stalls"`
	SequenceRegressions uint64    `json:"sequence_regressions"`
	LastMediaSequence   uint64    `json:"last_media_sequence"`
	LastProgressAt      time.Time `json:"last_progress_at,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveErrors   int       `json:"consecutive_errors"`
}

type ProcessSnapshot struct {
	UptimeSec    uint64             `json:"uptime_sec,omitempty"`
	Goroutines   int                `json:"goroutines,omitempty"`
	SessionCount int                `json:"session_count,omitempty"`
	Errors       uint64             `json:"errors,omitempty"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
}

type EndpointReport struct {
	Requests          uint64    `json:"requests"`
	Errors            uint64    `json:"errors"`
	ConsecutiveErrors int       `json:"consecutive_errors"`
	LastStatus        int       `json:"last_status,omitempty"`
	LastBytes         int64     `json:"last_bytes,omitempty"`
	LastCheckedAt     time.Time `json:"last_checked_at,omitempty"`
	LastError         string    `json:"last_error,omitempty"`
}

type Report struct {
	Type                  string          `json:"type"`
	StartedAt             time.Time       `json:"started_at"`
	EndedAt               time.Time       `json:"ended_at"`
	TargetDurationSeconds float64         `json:"target_duration_seconds"`
	DurationSeconds       float64         `json:"duration_seconds"`
	Failed                bool            `json:"failed"`
	Cancelled             bool            `json:"cancelled"`
	Failure               string          `json:"failure,omitempty"`
	Channels              []ChannelReport `json:"channels"`
	Process               ProcessSnapshot `json:"process,omitempty"`
	Status                EndpointReport  `json:"status"`
	Metrics               EndpointReport  `json:"metrics"`
	StatusRequests        uint64          `json:"status_requests"`
	MetricsRequests       uint64          `json:"metrics_requests"`
}

type Snapshot struct {
	Type            string          `json:"type"`
	At              time.Time       `json:"at"`
	ElapsedSeconds  float64         `json:"elapsed_seconds"`
	Channels        []ChannelReport `json:"channels"`
	Process         ProcessSnapshot `json:"process,omitempty"`
	StatusRequests  uint64          `json:"status_requests"`
	MetricsRequests uint64          `json:"metrics_requests"`
}

type renditionState struct {
	initialized       bool
	lastEndSequence   uint64
	lastAsset         string
	lastProgress      time.Time
	seenDiscontinuity map[uint64]struct{}
}

type channelState struct {
	report     ChannelReport
	renditions map[string]*renditionState
}

func New(cfg Config, options ...Option) (*Runner, error) {
	if cfg.Duration == 0 {
		cfg.Duration = defaultDuration
	}
	if cfg.Interval == 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.StallTimeout == 0 {
		cfg.StallTimeout = defaultStallTimeout
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.Concurrency == 0 {
		cfg.Concurrency = defaultConcurrency
	}
	if cfg.MaxConsecutiveErrors == 0 {
		cfg.MaxConsecutiveErrors = defaultMaxConsecutiveErrors
	}
	if cfg.Duration < 0 || cfg.Interval <= 0 || cfg.StallTimeout <= 0 || cfg.RequestTimeout <= 0 {
		return nil, fmt.Errorf("durations must be positive")
	}
	if cfg.Concurrency < 1 || cfg.MaxConsecutiveErrors < 1 {
		return nil, fmt.Errorf("concurrency and max consecutive errors must be positive")
	}
	baseURL, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
		return nil, fmt.Errorf("base URL must be an absolute HTTP URL")
	}
	if baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return nil, fmt.Errorf("base URL must not contain a query or fragment")
	}
	cfg.BaseURL = strings.TrimRight(baseURL.String(), "/")
	cfg.Channels = cleanChannels(cfg.Channels)
	runner := &Runner{
		cfg:     cfg,
		baseURL: baseURL,
		client:  &http.Client{},
		output:  io.Discard,
	}
	for _, option := range options {
		option(runner)
	}
	return runner, nil
}

func (r *Runner) Run(ctx context.Context) (Report, error) {
	startedAt := time.Now().UTC()
	report := Report{Type: "final", StartedAt: startedAt, TargetDurationSeconds: r.cfg.Duration.Seconds()}
	process := ProcessSnapshot{}

	if r.cfg.Token == "" && r.cfg.Username != "" {
		token, err := r.login(ctx)
		if err != nil {
			return r.finish(report, process, startedAt, err)
		}
		r.cfg.Token = token
	}
	channels := append([]string(nil), r.cfg.Channels...)
	if len(channels) == 0 {
		var err error
		channels, err = r.discoverChannels(ctx)
		if err != nil {
			return r.finish(report, process, startedAt, err)
		}
	}
	if len(channels) == 0 {
		return r.finish(report, process, startedAt, errors.New("no active channels found"))
	}
	for _, id := range channels {
		if strings.ContainsAny(id, `/\\`) {
			return r.finish(report, process, startedAt, fmt.Errorf("channel ID %q cannot be represented in a play URL", id))
		}
	}

	states := make([]*channelState, 0, len(channels))
	for _, id := range channels {
		states = append(states, &channelState{
			report:     ChannelReport{ID: id},
			renditions: make(map[string]*renditionState),
		})
	}
	r.copyChannelReports(&report, states)
	if err := r.writeSnapshot(startedAt, report, process); err != nil {
		return r.finish(report, process, startedAt, fmt.Errorf("write snapshot: %w", err))
	}

	deadline := time.NewTimer(r.cfg.Duration)
	defer deadline.Stop()
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()

	for {
		cycleErr := r.runCycle(ctx, states, &report, &process)
		r.copyChannelReports(&report, states)
		if err := r.writeSnapshot(startedAt, report, process); err != nil {
			cycleErr = errors.Join(cycleErr, fmt.Errorf("write snapshot: %w", err))
		}
		if cycleErr != nil {
			return r.finish(report, process, startedAt, cycleErr)
		}
		select {
		case <-ctx.Done():
			return r.finish(report, process, startedAt, ctx.Err())
		case <-deadline.C:
			return r.finish(report, process, startedAt, nil)
		case <-ticker.C:
		}
	}
}

func (r *Runner) runCycle(ctx context.Context, states []*channelState, report *Report, process *ProcessSnapshot) error {
	semaphore := make(chan struct{}, min(r.cfg.Concurrency, len(states)))
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var cycleErr error
	for _, state := range states {
		state := state
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				return
			}
			err := r.checkChannel(ctx, state)
			if err != nil {
				errMu.Lock()
				if cycleErr == nil || errors.Is(err, ErrStalled) {
					cycleErr = err
				}
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := r.checkOptionalEndpoint(ctx, r.cfg.StatusPath, &report.Status, process, false); err != nil && cycleErr == nil {
		if report.Status.ConsecutiveErrors >= r.cfg.MaxConsecutiveErrors {
			cycleErr = fmt.Errorf("status endpoint: %w: %v", ErrTooManyErrors, err)
		}
	}
	if err := r.checkOptionalEndpoint(ctx, r.cfg.MetricsPath, &report.Metrics, process, true); err != nil && cycleErr == nil {
		if report.Metrics.ConsecutiveErrors >= r.cfg.MaxConsecutiveErrors {
			cycleErr = fmt.Errorf("metrics endpoint: %w: %v", ErrTooManyErrors, err)
		}
	}
	report.StatusRequests = report.Status.Requests
	report.MetricsRequests = report.Metrics.Requests
	report.Process = cloneProcess(*process)
	return cycleErr
}

func (r *Runner) checkChannel(ctx context.Context, state *channelState) error {
	indexURL := r.endpoint("/v1/play/" + state.report.ID + "/index.m3u8")
	body, _, err := r.get(ctx, indexURL)
	state.report.PlaylistRequests++
	if err != nil {
		return r.recordChannelError(state, err)
	}
	state.report.Bytes += uint64(len(body))
	parsed := parsePlaylist(body)
	playlistTargets := []playlistReference{{URI: indexURL}}
	if parsed.Master {
		playlistTargets = playlistTargets[:0]
		for _, reference := range parsed.References {
			resolved, resolveErr := resolveReference(indexURL, reference.URI)
			if resolveErr != nil {
				return r.recordChannelError(state, resolveErr)
			}
			reference.URI = resolved
			playlistTargets = append(playlistTargets, reference)
		}
		if len(playlistTargets) == 0 {
			return r.recordChannelError(state, errors.New("master playlist has no media renditions"))
		}
	}

	now := time.Now().UTC()
	for _, target := range uniquePlaylistReferences(playlistTargets) {
		playlistURL := target.URI
		mediaBody := body
		if playlistURL != indexURL {
			mediaBody, _, err = r.get(ctx, playlistURL)
			state.report.PlaylistRequests++
			if err != nil {
				return r.recordChannelError(state, err)
			}
			state.report.Bytes += uint64(len(mediaBody))
		}
		media, mediaErr := parseMediaPlaylist(mediaBody)
		if mediaErr != nil {
			if target.AllowEmpty && errors.Is(mediaErr, errNoMediaAssets) {
				continue
			}
			return r.recordChannelError(state, fmt.Errorf("parse media playlist: %w", mediaErr))
		}
		rendition := state.renditions[playlistURL]
		if rendition == nil {
			rendition = &renditionState{seenDiscontinuity: make(map[uint64]struct{})}
			state.renditions[playlistURL] = rendition
		}
		if rendition.initialized && media.EndSequence < rendition.lastEndSequence {
			state.report.SequenceRegressions++
			state.report.LastError = fmt.Sprintf("rendition %s moved backward from %d to %d",
				redactURL(playlistURL), rendition.lastEndSequence, media.EndSequence)
			return fmt.Errorf("%w: channel %s", ErrSequenceRegressed, state.report.ID)
		}
		progressed := !rendition.initialized || media.EndSequence > rendition.lastEndSequence || media.LatestAsset != rendition.lastAsset
		if progressed {
			rendition.initialized = true
			rendition.lastEndSequence = media.EndSequence
			rendition.lastAsset = media.LatestAsset
			rendition.lastProgress = now
			state.report.ProgressEvents++
			state.report.LastProgressAt = now
			if media.EndSequence > state.report.LastMediaSequence {
				state.report.LastMediaSequence = media.EndSequence
			}
		} else if now.Sub(rendition.lastProgress) >= r.cfg.StallTimeout {
			state.report.Stalls++
			state.report.LastError = fmt.Sprintf("rendition %s did not advance for %s", redactURL(playlistURL), r.cfg.StallTimeout)
			return fmt.Errorf("%w: channel %s", ErrStalled, state.report.ID)
		}
		for _, sequence := range media.DiscontinuitySequences {
			if _, seen := rendition.seenDiscontinuity[sequence]; !seen {
				rendition.seenDiscontinuity[sequence] = struct{}{}
				state.report.Discontinuities++
			}
		}
		assetURL, resolveErr := resolveReference(playlistURL, media.LatestAsset)
		if resolveErr != nil {
			return r.recordChannelError(state, resolveErr)
		}
		assetBytes, fetchErr := r.getAsset(ctx, assetURL)
		state.report.SegmentRequests++
		if fetchErr != nil {
			return r.recordChannelError(state, fetchErr)
		}
		state.report.Bytes += uint64(assetBytes)
	}
	state.report.ConsecutiveErrors = 0
	return nil
}

func (r *Runner) recordChannelError(state *channelState, err error) error {
	state.report.HTTPErrors++
	state.report.ConsecutiveErrors++
	state.report.LastError = redactError(err)
	if state.report.ConsecutiveErrors >= r.cfg.MaxConsecutiveErrors {
		return fmt.Errorf("channel %s: %w: %v", state.report.ID, ErrTooManyErrors, err)
	}
	return nil
}

func (r *Runner) checkOptionalEndpoint(ctx context.Context, endpointPath string, report *EndpointReport, process *ProcessSnapshot, metrics bool) error {
	if strings.TrimSpace(endpointPath) == "" {
		return nil
	}
	body, status, err := r.get(ctx, r.endpoint(endpointPath))
	report.Requests++
	report.LastCheckedAt = time.Now().UTC()
	report.LastStatus = status
	if err != nil {
		report.Errors++
		report.ConsecutiveErrors++
		report.LastError = redactError(err)
		return err
	}
	report.LastBytes = int64(len(body))
	if metrics {
		report.ConsecutiveErrors = 0
		process.Metrics = parsePrometheusMetrics(body)
		return nil
	}
	var statusSnapshot struct {
		UptimeSec    uint64 `json:"uptime_sec"`
		Goroutines   int    `json:"goroutines"`
		SessionCount int    `json:"session_count"`
		Errors       uint64 `json:"errors"`
	}
	if err := json.Unmarshal(body, &statusSnapshot); err != nil {
		report.Errors++
		report.ConsecutiveErrors++
		report.LastError = "invalid status JSON"
		return err
	}
	report.ConsecutiveErrors = 0
	process.UptimeSec = statusSnapshot.UptimeSec
	process.Goroutines = statusSnapshot.Goroutines
	process.SessionCount = statusSnapshot.SessionCount
	process.Errors = statusSnapshot.Errors
	return nil
}

func (r *Runner) discoverChannels(ctx context.Context) ([]string, error) {
	body, _, err := r.get(ctx, r.endpoint("/v1/channels"))
	if err != nil {
		return nil, fmt.Errorf("discover channels: %w", err)
	}
	var response struct {
		Channels []struct {
			ID string `json:"id"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode channels: %w", err)
	}
	channels := make([]string, 0, len(response.Channels))
	for _, channel := range response.Channels {
		channels = append(channels, channel.ID)
	}
	return cleanChannels(channels), nil
}

func (r *Runner) login(ctx context.Context) (string, error) {
	payload, err := json.Marshal(map[string]string{"username": r.cfg.Username, "password": r.cfg.Password})
	if err != nil {
		return "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, r.endpoint("/v1/auth/login"), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("login returned HTTP %d", resp.StatusCode)
	}
	if len(body) > maxResponseBytes {
		return "", fmt.Errorf("login response exceeded %d bytes", maxResponseBytes)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil || result.Token == "" {
		return "", errors.New("login response has no token")
	}
	return result.Token, nil
}

func (r *Runner) get(ctx context.Context, endpointURL string) ([]byte, int, error) {
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return nil, 0, err
	}
	if r.cfg.Token != "" && r.sameOrigin(req.URL) {
		req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	}
	req.Header.Set("User-Agent", "kiln-soak/1")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, requestError("GET", endpointURL, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: %w", redactURL(endpointURL), readErr)
	}
	if len(body) > maxResponseBytes {
		return nil, resp.StatusCode, fmt.Errorf("GET %s exceeded %d bytes", redactURL(endpointURL), maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("GET %s returned HTTP %d", redactURL(endpointURL), resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func (r *Runner) getAsset(ctx context.Context, endpointURL string) (int64, error) {
	requestCtx, cancel := context.WithTimeout(ctx, r.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return 0, err
	}
	if r.cfg.Token != "" && r.sameOrigin(req.URL) {
		req.Header.Set("Authorization", "Bearer "+r.cfg.Token)
	}
	req.Header.Set("User-Agent", "kiln-soak/1")
	resp, err := r.client.Do(req)
	if err != nil {
		return 0, requestError("GET", endpointURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return 0, fmt.Errorf("GET %s returned HTTP %d", redactURL(endpointURL), resp.StatusCode)
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxAssetBytes+1))
	if err != nil {
		return n, requestError("GET", endpointURL, err)
	}
	if n > maxAssetBytes {
		return n, fmt.Errorf("GET %s exceeded %d bytes", redactURL(endpointURL), maxAssetBytes)
	}
	return n, nil
}

func (r *Runner) sameOrigin(candidate *url.URL) bool {
	return strings.EqualFold(candidate.Scheme, r.baseURL.Scheme) && strings.EqualFold(candidate.Host, r.baseURL.Host)
}

func (r *Runner) endpoint(endpointPath string) string {
	if parsed, err := url.Parse(endpointPath); err == nil && parsed.IsAbs() {
		return parsed.String()
	}
	base := *r.baseURL
	base.Path = strings.TrimRight(base.Path, "/") + "/" + strings.TrimLeft(endpointPath, "/")
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	return base.String()
}

func (r *Runner) copyChannelReports(report *Report, states []*channelState) {
	report.Channels = make([]ChannelReport, len(states))
	for i, state := range states {
		report.Channels[i] = state.report
	}
}

func (r *Runner) writeSnapshot(startedAt time.Time, report Report, process ProcessSnapshot) error {
	snapshot := Snapshot{
		Type:            "snapshot",
		At:              time.Now().UTC(),
		ElapsedSeconds:  time.Since(startedAt).Seconds(),
		Channels:        append([]ChannelReport(nil), report.Channels...),
		Process:         process,
		StatusRequests:  report.StatusRequests,
		MetricsRequests: report.MetricsRequests,
	}
	return r.writeJSON(snapshot)
}

func (r *Runner) finish(report Report, process ProcessSnapshot, startedAt time.Time, runErr error) (Report, error) {
	report.EndedAt = time.Now().UTC()
	report.DurationSeconds = report.EndedAt.Sub(startedAt).Seconds()
	report.Process = cloneProcess(process)
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		report.Cancelled = true
	} else if runErr != nil {
		report.Failed = true
		report.Failure = redactError(runErr)
	}
	if err := r.writeJSON(report); err != nil {
		writeErr := fmt.Errorf("write final report: %w", err)
		if runErr == nil {
			report.Failed = true
			report.Failure = redactError(writeErr)
		}
		return report, errors.Join(runErr, writeErr)
	}
	return report, runErr
}

func cloneProcess(process ProcessSnapshot) ProcessSnapshot {
	if process.Metrics != nil {
		metrics := make(map[string]float64, len(process.Metrics))
		for name, value := range process.Metrics {
			metrics[name] = value
		}
		process.Metrics = metrics
	}
	return process
}

func (r *Runner) writeJSON(value any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return json.NewEncoder(r.output).Encode(value)
}

type parsedPlaylist struct {
	Master     bool
	References []playlistReference
}

type playlistReference struct {
	URI        string
	AllowEmpty bool
}

func parsePlaylist(body []byte) parsedPlaylist {
	var result parsedPlaylist
	var nextVariant bool
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			result.Master = true
			nextVariant = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			result.Master = true
			if reference := attributeValue(line, "URI"); reference != "" {
				result.References = append(result.References, playlistReference{
					URI:        reference,
					AllowEmpty: strings.EqualFold(attributeValue(line, "TYPE"), "SUBTITLES"),
				})
			}
		case line != "" && !strings.HasPrefix(line, "#") && nextVariant:
			result.References = append(result.References, playlistReference{URI: line})
			nextVariant = false
		}
	}
	result.References = uniquePlaylistReferences(result.References)
	return result
}

func uniquePlaylistReferences(references []playlistReference) []playlistReference {
	seen := make(map[string]int, len(references))
	result := make([]playlistReference, 0, len(references))
	for _, reference := range references {
		if index, ok := seen[reference.URI]; ok {
			result[index].AllowEmpty = result[index].AllowEmpty && reference.AllowEmpty
			continue
		}
		seen[reference.URI] = len(result)
		result = append(result, reference)
	}
	return result
}

type mediaPlaylist struct {
	EndSequence            uint64
	LatestAsset            string
	DiscontinuitySequences []uint64
}

func parseMediaPlaylist(body []byte) (mediaPlaylist, error) {
	var result mediaPlaylist
	var sequence uint64
	var segmentCount uint64
	var discontinuityPending bool
	var latestPart string
	var sawHeader bool
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case line == "#EXTM3U":
			sawHeader = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			parsed, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return result, fmt.Errorf("invalid media sequence %q", value)
			}
			sequence = parsed
		case line == "#EXT-X-DISCONTINUITY":
			discontinuityPending = true
		case strings.HasPrefix(line, "#EXT-X-PART:"):
			if reference := attributeValue(line, "URI"); reference != "" {
				latestPart = reference
				result.LatestAsset = reference
				if discontinuityPending {
					result.DiscontinuitySequences = append(result.DiscontinuitySequences, sequence+segmentCount)
					discontinuityPending = false
				}
			}
		case line != "" && !strings.HasPrefix(line, "#"):
			if discontinuityPending {
				result.DiscontinuitySequences = append(result.DiscontinuitySequences, sequence+segmentCount)
				discontinuityPending = false
			}
			result.LatestAsset = line
			segmentCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}
	if !sawHeader {
		return result, errors.New("missing EXTM3U header")
	}
	if result.LatestAsset == "" {
		result.LatestAsset = latestPart
	}
	if result.LatestAsset == "" {
		return result, errNoMediaAssets
	}
	if segmentCount > 0 {
		result.EndSequence = sequence + segmentCount - 1
	} else {
		result.EndSequence = sequence
	}
	return result, nil
}

func attributeValue(line, key string) string {
	attributes := line
	if colon := strings.IndexByte(attributes, ':'); colon >= 0 {
		attributes = attributes[colon+1:]
	}
	for len(attributes) > 0 {
		end := len(attributes)
		quoted := false
		for index, char := range attributes {
			switch char {
			case '"':
				quoted = !quoted
			case ',':
				if !quoted {
					end = index
				}
			}
			if end != len(attributes) {
				break
			}
		}
		attribute := strings.TrimSpace(attributes[:end])
		name, value, ok := strings.Cut(attribute, "=")
		if ok && strings.EqualFold(strings.TrimSpace(name), key) {
			value = strings.TrimSpace(value)
			if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
				return value[1 : len(value)-1]
			}
			return value
		}
		if end == len(attributes) {
			break
		}
		attributes = attributes[end+1:]
	}
	return ""
}

func resolveReference(baseURL, reference string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(strings.TrimSpace(reference))
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("unsupported media URL scheme %q", resolved.Scheme)
	}
	return resolved.String(), nil
}

func parsePrometheusMetrics(body []byte) map[string]float64 {
	allowed := map[string]struct{}{
		"kiln_uptime_seconds":      {},
		"kiln_goroutines":          {},
		"kiln_sessions":            {},
		"kiln_errors_total":        {},
		"kiln_http_requests_total": {},
		"kiln_bytes_in_total":      {},
		"kiln_bytes_out_total":     {},
	}
	result := make(map[string]float64)
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			continue
		}
		series := fields[0]
		name := series
		if index := strings.IndexByte(name, '{'); index >= 0 {
			name = name[:index]
		}
		if _, ok := allowed[name]; !ok && !strings.HasPrefix(name, "kiln_packager_") {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err == nil {
			result[series] = value
		}
	}
	return result
}

func cleanChannels(channels []string) []string {
	cleaned := make([]string, 0, len(channels))
	for _, channel := range channels {
		if channel = strings.TrimSpace(channel); channel != "" {
			cleaned = append(cleaned, channel)
		}
	}
	sort.Strings(cleaned)
	return uniqueStrings(cleaned)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func redactURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return path.Base(raw)
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func redactError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if parsed, parseErr := url.Parse(message); parseErr == nil && parsed.RawQuery != "" {
		parsed.RawQuery = ""
		message = parsed.String()
	}
	return message
}

func requestError(method, rawURL string, err error) error {
	cause := "request failed"
	switch {
	case errors.Is(err, context.Canceled):
		cause = context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		cause = context.DeadlineExceeded.Error()
	}
	return fmt.Errorf("%s %s: %s", method, redactURL(rawURL), cause)
}
