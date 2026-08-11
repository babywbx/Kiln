package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
)

var (
	ErrNotFound    = apperr.New(apperr.CodeNotFound, 404, "channel not found")
	ErrViewerLease = apperr.New(apperr.CodeForbidden, 403, "viewer lease expired")
	ErrViewerLimit = apperr.New(apperr.CodeTooMany, 429, "channel viewer limit reached")
)

const (
	maxDashRestarts   = 8
	restartBaseDelay  = 2 * time.Second
	restartMaxDelay   = 30 * time.Second
	restartResetAfter = 90 * time.Second
	shutdownTimeout   = 15 * time.Second
	viewerLeaseTTL    = time.Minute
)

type RestartPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	ResetAfter  time.Duration
}

type Catalog interface {
	Config() config.File
	ActiveChannels() []config.Channel
	Get(string) (config.Channel, bool)
	SourceURL(config.Channel) (string, error)
	Upstream(config.Channel) (config.Upstream, error)
}

func defaultRestartPolicy() RestartPolicy {
	return RestartPolicy{
		MaxAttempts: maxDashRestarts,
		BaseDelay:   restartBaseDelay,
		MaxDelay:    restartMaxDelay,
		ResetAfter:  restartResetAfter,
	}
}

type spawnGate chan struct{}

func newSpawnGate(n int) spawnGate {
	if n <= 0 {
		n = 1
	}
	return make(spawnGate, n)
}

func (g spawnGate) Acquire(ctx context.Context) (func(), error) {
	select {
	case g <- struct{}{}:
		return func() { <-g }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type Manager struct {
	cat             Catalog
	pull            *pull.Client
	obs             *observe.Service
	egress          *proxyegress.Router
	dataDir         string
	ffmpeg          config.FFmpeg
	keys            []config.KeyPair
	log             *slog.Logger
	spawn           spawnGate
	pack            packager.Packager
	ffmpegAvailable bool

	mu              sync.Mutex
	sessions        map[string]*Session
	inflight        map[string]*startWait
	reloading       map[string]struct{}
	reloadPending   map[string]struct{}
	viewers         map[string]map[string]time.Time
	closing         bool
	restart         RestartPolicy
	watchers        sync.WaitGroup
	lifecycleCtx    context.Context
	lifecycleCancel context.CancelFunc
	shutdownOnce    sync.Once
	shutdownDone    chan struct{}
}

type startWait struct {
	done    chan struct{}
	sess    *Session
	err     error
	cancel  context.CancelFunc
	stopped bool
}

type Session struct {
	Channel   config.Channel
	SourceURL string
	Upstream  config.Upstream
	Mode      string
	StartedAt time.Time

	mu             sync.RWMutex
	lastTouch      time.Time
	workDir        string
	errors         int
	lastError      string
	engine         string
	packMode       string
	fallbackReason string
	state          string
	job            packager.Job
	publication    packager.Publication
	generation     string
	restarts       int
	lastOK         time.Time

	cancel context.CancelFunc
	ctx    context.Context
}

func (s *Session) Publication() packager.Publication {
	publication, _ := s.PublicationSnapshot()
	return publication
}

func (s *Session) PublicationSnapshot() (packager.Publication, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publication, s.generation
}

func (s *Session) State() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Session) SourceSnapshot() (config.Channel, string, config.Upstream, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Channel, s.SourceURL, s.Upstream, s.Mode
}

func NewManager(cat Catalog, pullClient *pull.Client, obs *observe.Service, dataDir string, ff config.FFmpeg, keys []config.KeyPair, log *slog.Logger, egress *proxyegress.Router) *Manager {
	m := newManager(cat, pullClient, obs, dataDir, ff, keys, log, egress)
	m.pack = m.newPackager()
	return m
}

func NewNativeManager(cat Catalog, pullClient *pull.Client, obs *observe.Service, dataDir string, ff config.FFmpeg, keys []config.KeyPair, log *slog.Logger, egress *proxyegress.Router) *Manager {
	m := newManager(cat, pullClient, obs, dataDir, ff, keys, log, egress)
	m.pack = m.newNativePackager()
	return m
}

func newManager(cat Catalog, pullClient *pull.Client, obs *observe.Service, dataDir string, ff config.FFmpeg, keys []config.KeyPair, log *slog.Logger, egress *proxyegress.Router) *Manager {
	if log == nil {
		log = slog.Default()
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	m := &Manager{
		cat:             cat,
		pull:            pullClient,
		obs:             obs,
		egress:          egress,
		dataDir:         dataDir,
		ffmpeg:          ff,
		keys:            append([]config.KeyPair(nil), keys...),
		log:             log,
		spawn:           newSpawnGate(ff.MaxStarts),
		sessions:        map[string]*Session{},
		inflight:        map[string]*startWait{},
		reloading:       map[string]struct{}{},
		reloadPending:   map[string]struct{}{},
		viewers:         map[string]map[string]time.Time{},
		restart:         defaultRestartPolicy(),
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		shutdownDone:    make(chan struct{}),
	}
	return m
}

func (m *Manager) newPackager() packager.Packager {
	native := m.newNativePackager()
	var ffmpeg packager.Packager
	if err := packager.CheckFFmpegDependency(m.ffmpeg); err != nil {
		m.log.Info("ffmpeg compatibility engine unavailable",
			"dependency", m.ffmpeg.Dependency(), "err", err)
	} else {
		m.ffmpegAvailable = true
		ffmpeg = packager.NewFFmpegAdapter(m.ffmpeg, m.pull, m.egress, m.spawn)
	}
	return packager.NewAdaptivePackager(native, ffmpeg, m.log)
}

func (m *Manager) newNativePackager() *packager.NativeAdapter {
	cfg := m.cat.Config().Packager
	native := packager.NewNativeAdapter(
		packager.NewPullFetcher(m.pull, cfg.MaxSegmentBytes),
		cfg.PlaylistSize,
		time.Duration(cfg.GraceSec)*time.Second,
	)
	native.StartSegments = cfg.StartSegments
	native.Prefetch = cfg.PrefetchSegments
	native.MaxSegmentBytes = cfg.MaxSegmentBytes
	native.PrimaryTrackHold = time.Duration(cfg.PrimaryTrackHoldSec) * time.Second
	native.StallTimeout = time.Duration(cfg.StallTimeoutSec) * time.Second
	native.LLHLS = cfg.LLHLS
	native.PartTarget = time.Duration(cfg.PartTargetMS) * time.Millisecond
	native.SetInflightBytes(cfg.InflightBytes)
	return native
}

func (m *Manager) FFmpegAvailable() bool {
	return m.ffmpegAvailable
}

func (m *Manager) SetPackager(p packager.Packager) {
	m.mu.Lock()
	m.pack = p
	m.mu.Unlock()
}

func (m *Manager) SetRestartPolicy(policy RestartPolicy) {
	defaults := defaultRestartPolicy()
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = defaults.MaxAttempts
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = defaults.BaseDelay
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = defaults.MaxDelay
	}
	if policy.MaxDelay < policy.BaseDelay {
		policy.MaxDelay = policy.BaseDelay
	}
	if policy.ResetAfter <= 0 {
		policy.ResetAfter = defaults.ResetAfter
	}
	m.mu.Lock()
	m.restart = policy
	m.mu.Unlock()
}

func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return
	}
	m.watchers.Add(2)
	m.mu.Unlock()
	go func() {
		defer m.watchers.Done()
		m.reaper(ctx)
	}()
	go func() {
		defer m.watchers.Done()
		m.autostart(ctx)
	}()
}

func (m *Manager) Pull() *pull.Client { return m.pull }

func (m *Manager) Warmup(channelID string) error {
	ch, src, up, err := m.resolve(channelID)
	if err != nil {
		return err
	}

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return context.Canceled
	}
	if s, ok := m.sessions[channelID]; ok {
		m.touch(s)
		m.mu.Unlock()
		return nil
	}
	if _, ok := m.inflight[channelID]; ok {
		m.mu.Unlock()
		return nil
	}
	w, s := m.beginStartLocked(ch, src, up)
	m.mu.Unlock()

	go func() {
		if _, err := m.finishStart(channelID, w, s); err != nil && err != context.Canceled {
			m.log.Error("session warmup failed", "channel", channelID, "err", err)
		}
	}()
	return nil
}

func (m *Manager) ReloadChannel(channelID string) bool {
	ch, src, up, err := m.resolve(channelID)
	if err != nil || ch.Disabled {
		return false
	}
	m.mu.Lock()
	s, ok := m.sessions[channelID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	if _, busy := m.reloading[channelID]; busy {
		m.reloadPending[channelID] = struct{}{}
		m.mu.Unlock()
		return true
	}
	s.mu.RLock()
	oldJob := s.job
	oldWorkDir := s.workDir
	s.mu.RUnlock()
	if ch.Ingress != "dash" {
		s.mu.Lock()
		s.Channel = ch
		s.SourceURL = src
		s.Upstream = up
		s.Mode = ch.Ingress
		s.job = nil
		s.publication = nil
		s.workDir = ""
		s.engine = ""
		s.packMode = ""
		s.fallbackReason = ""
		s.lastError = ""
		s.lastOK = time.Now()
		s.state = "running"
		s.mu.Unlock()
		m.publish(s)
		if oldJob != nil || oldWorkDir != "" {
			m.watchers.Add(1)
		}
		m.mu.Unlock()
		if oldJob != nil || oldWorkDir != "" {
			go func() {
				defer m.watchers.Done()
				cleanupJob(oldJob, oldWorkDir)
			}()
		}
		return true
	}
	m.reloading[channelID] = struct{}{}
	candidate := &Session{Channel: ch, SourceURL: src, Upstream: up, Mode: ch.Ingress, StartedAt: s.StartedAt, ctx: s.ctx}
	m.watchers.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.watchers.Done()
		started, startErr := m.launchDash(candidate)
		m.mu.Lock()
		delete(m.reloading, channelID)
		_, pending := m.reloadPending[channelID]
		delete(m.reloadPending, channelID)
		if startErr != nil {
			current := m.currentJobLocked(channelID, s, oldJob)
			m.mu.Unlock()
			if current {
				m.log.Error("channel reload failed; keeping current publication", "channel", channelID, "err", startErr)
			}
			if pending {
				m.ReloadChannel(channelID)
			}
			return
		}
		if !m.currentJobLocked(channelID, s, oldJob) {
			m.mu.Unlock()
			cleanupDashStart(started)
			if pending {
				m.ReloadChannel(channelID)
			}
			return
		}
		s.mu.Lock()
		oldWorkDir := s.workDir
		s.Channel = ch
		s.SourceURL = src
		s.Upstream = up
		s.Mode = ch.Ingress
		s.installLocked(started)
		s.mu.Unlock()
		m.publish(s)
		if ch.RestartOnFailure {
			m.startWatcherLocked(channelID, s, started.job)
		}
		m.mu.Unlock()
		cleanupJob(oldJob, oldWorkDir)
		if pending {
			m.ReloadChannel(channelID)
		}
	}()
	return true
}

func (m *Manager) Acquire(channelID string) (*Session, error) {
	ch, src, up, err := m.resolve(channelID)
	if err != nil {
		return nil, err
	}

	for {
		m.mu.Lock()
		if m.closing {
			m.mu.Unlock()
			return nil, context.Canceled
		}
		if s, ok := m.sessions[channelID]; ok {
			m.touch(s)
			m.mu.Unlock()
			return s, nil
		}
		if w, ok := m.inflight[channelID]; ok {
			m.mu.Unlock()
			<-w.done
			if w.err != nil {
				return nil, w.err
			}
			if w.sess != nil {
				m.mu.Lock()
				if cur, ok := m.sessions[channelID]; ok {
					m.touch(cur)
					m.mu.Unlock()
					return cur, nil
				}
				m.mu.Unlock()
				if w.sess != nil {
					return w.sess, nil
				}
			}
			continue
		}
		w, s := m.beginStartLocked(ch, src, up)
		m.mu.Unlock()
		return m.finishStart(channelID, w, s)
	}
}

func (m *Manager) AdmitViewer(channelID, viewerID string) error {
	ch, ok := m.cat.Get(channelID)
	if !ok {
		return ErrNotFound
	}
	if ch.MaxViewers <= 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	active := m.activeViewersLocked(channelID, now)
	if active == nil {
		active = map[string]time.Time{}
		m.viewers[channelID] = active
	}
	if _, ok := active[viewerID]; !ok && len(active) >= ch.MaxViewers {
		return ErrViewerLimit
	}
	active[viewerID] = now
	return nil
}

func (m *Manager) RefreshViewer(channelID, viewerID string) error {
	ch, ok := m.cat.Get(channelID)
	if !ok {
		return ErrNotFound
	}
	if ch.MaxViewers <= 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	active := m.activeViewersLocked(channelID, now)
	if _, ok := active[viewerID]; !ok {
		return ErrViewerLease
	}
	active[viewerID] = now
	return nil
}

func (m *Manager) activeViewersLocked(channelID string, now time.Time) map[string]time.Time {
	active := m.viewers[channelID]
	cutoff := now.Add(-viewerLeaseTTL)
	for id, touchedAt := range active {
		if touchedAt.Before(cutoff) {
			delete(active, id)
		}
	}
	return active
}

func (m *Manager) resolve(channelID string) (config.Channel, string, config.Upstream, error) {
	ch, ok := m.cat.Get(channelID)
	if !ok {
		return config.Channel{}, "", config.Upstream{}, ErrNotFound
	}
	src, err := m.cat.SourceURL(ch)
	if err != nil {
		return config.Channel{}, "", config.Upstream{}, err
	}
	up, err := m.cat.Upstream(ch)
	if err != nil {
		return config.Channel{}, "", config.Upstream{}, err
	}
	return ch, src, up, nil
}

func (m *Manager) beginStartLocked(ch config.Channel, src string, up config.Upstream) (*startWait, *Session) {
	sctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	s := &Session{
		Channel:    ch,
		SourceURL:  src,
		Upstream:   up,
		Mode:       ch.Ingress,
		StartedAt:  now,
		lastTouch:  now,
		state:      "starting",
		generation: newGeneration(),
		cancel:     cancel,
		ctx:        sctx,
	}
	w := &startWait{done: make(chan struct{}), sess: s, cancel: cancel}
	m.inflight[ch.ID] = w
	m.publish(s)
	return w, s
}

func (m *Manager) finishStart(channelID string, w *startWait, s *Session) (*Session, error) {
	var started *dashStart
	var startErr error
	if s.Channel.Ingress == "dash" {
		started, startErr = m.launchDash(s)
	}

	m.mu.Lock()
	current := m.inflight[channelID] == w
	if current {
		delete(m.inflight, channelID)
	}
	if w.stopped || !current {
		w.cancel()
		w.err = context.Canceled
		m.mu.Unlock()
		cleanupDashStart(started)
		close(w.done)
		return nil, context.Canceled
	}
	if startErr != nil {
		w.cancel()
		s.mu.Lock()
		s.lastError = startErr.Error()
		s.state = "failed"
		s.mu.Unlock()
		m.publish(s)
		w.err = startErr
		close(w.done)
		m.mu.Unlock()
		return nil, startErr
	}
	if existing, ok := m.sessions[channelID]; ok {
		w.cancel()
		m.touch(existing)
		w.sess = existing
		m.mu.Unlock()
		cleanupDashStart(started)
		close(w.done)
		return existing, nil
	}
	if started != nil {
		s.install(started)
	} else {
		s.mu.Lock()
		s.state = "running"
		s.lastOK = time.Now()
		s.mu.Unlock()
	}
	m.sessions[channelID] = s
	m.publish(s)
	if started != nil && s.Channel.RestartOnFailure {
		m.startWatcherLocked(channelID, s, started.job)
	}
	engine, packMode, fallbackReason := "", "", ""
	if started != nil {
		engine = started.engine
		packMode = started.packMode
		fallbackReason = started.fallbackReason
	}
	close(w.done)
	m.mu.Unlock()
	channel, _, _, _ := s.SourceSnapshot()
	m.log.Info("session started", "channel", channelID, "ingress", channel.Ingress,
		"engine", engine, "pack_mode", packMode, "fallback_reason", fallbackReason)
	return s, nil
}

type dashStart struct {
	job            packager.Job
	publication    packager.Publication
	generation     string
	workDir        string
	engine         string
	packMode       string
	fallbackReason string
}

func (m *Manager) launchDash(s *Session) (*dashStart, error) {
	ch, sourceURL, upstream, _ := s.SourceSnapshot()
	if err := config.ValidateChannelID(ch.ID); err != nil {
		return nil, apperr.Wrap(apperr.CodeInvalid, 400, "invalid channel id", err)
	}
	if len(m.keys) == 0 {
		return nil, apperr.New(apperr.CodeInvalid, 400, "no global keys configured")
	}
	keys := append([]config.KeyPair(nil), m.keys...)
	generation := newGeneration()
	work := filepath.Join(m.dataDir, "sessions", ch.ID, generation)
	headers := maps.Clone(upstream.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	maps.Copy(headers, ch.Headers)

	prefer := m.ffmpeg.PreferHeight
	if ch.PreferHeight > 0 {
		prefer = ch.PreferHeight
	}
	m.mu.Lock()
	engine := m.pack
	m.mu.Unlock()
	job, err := engine.Start(s.ctx, packager.Request{
		ChannelID:                ch.ID,
		SourceURL:                sourceURL,
		Keys:                     keys,
		Headers:                  headers,
		UserAgent:                ch.UserAgent,
		WorkDir:                  work,
		PreferHeight:             prefer,
		PreferredAudioLanguages:  append([]string(nil), ch.PreferredAudioLanguages...),
		Selection:                ch.Selection,
		Engine:                   m.cat.Config().EngineFor(ch),
		UpgradeInsecureRedirects: m.cat.Config().UpgradeInsecureRedirectsFor(ch),
		Log:                      m.log.With("channel", ch.ID),
	})
	if err != nil {
		_ = os.RemoveAll(work)
		if _, ok := apperr.As(err); ok {
			return nil, err
		}
		return nil, apperr.Wrap(apperr.CodeUpstream, 502, "dash packager failed", err)
	}
	if job == nil {
		_ = os.RemoveAll(work)
		return nil, apperr.New(apperr.CodeUpstream, 502, "dash packager returned no job")
	}
	return &dashStart{
		job:            job,
		publication:    job.Publication(),
		generation:     generation,
		workDir:        work,
		engine:         job.Engine(),
		packMode:       job.PackMode(),
		fallbackReason: job.FallbackReason(),
	}, nil
}

func (s *Session) install(started *dashStart) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installLocked(started)
}

func (s *Session) installLocked(started *dashStart) {
	s.job = started.job
	s.publication = started.publication
	s.generation = started.generation
	s.workDir = started.workDir
	s.engine = started.engine
	s.packMode = started.packMode
	s.fallbackReason = started.fallbackReason
	s.lastError = ""
	s.lastOK = time.Now()
	s.state = "running"
}

func (m *Manager) startWatcherLocked(channelID string, s *Session, job packager.Job) {
	m.watchers.Add(1)
	go func() {
		defer m.watchers.Done()
		m.watchJob(channelID, s, job)
	}()
}

func (m *Manager) watchJob(channelID string, s *Session, failedJob packager.Job) {
	select {
	case <-failedJob.Done():
	case <-s.ctx.Done():
		return
	}
	if failedJob.IntentionalStop() {
		return
	}

	m.mu.Lock()
	if !m.currentJobLocked(channelID, s, failedJob) {
		m.mu.Unlock()
		return
	}
	channel, _, _, _ := s.SourceSnapshot()
	if channel.OnDemand {
		idle := m.cat.Config().IdleTimeout(channel)
		s.mu.RLock()
		lastTouch := s.lastTouch
		s.mu.RUnlock()
		if time.Since(lastTouch) > idle {
			m.log.Info("session idle end", "channel", channelID)
			cleanup := m.detachLocked(channelID, s, true)
			m.mu.Unlock()
			cleanup.run()
			return
		}
	}
	policy := m.restart
	failure := errString(failedJob.Err())
	s.mu.Lock()
	if !s.lastOK.IsZero() && time.Since(s.lastOK) >= policy.ResetAfter {
		s.restarts = 0
	}
	s.errors++
	s.lastError = failure
	s.state = "restarting"
	s.mu.Unlock()
	m.publish(s)
	m.mu.Unlock()

	for {
		m.mu.Lock()
		if !m.currentJobLocked(channelID, s, failedJob) {
			m.mu.Unlock()
			return
		}
		s.mu.Lock()
		if s.restarts >= policy.MaxAttempts {
			s.state = "failed"
			if s.lastError == "" {
				s.lastError = failure
			}
			s.mu.Unlock()
			cleanup := m.detachFailedLocked(channelID, s)
			m.log.Error("session restart budget exceeded", "channel", channelID,
				"attempts", policy.MaxAttempts, "err", failure)
			m.mu.Unlock()
			cleanup.run()
			return
		}
		s.restarts++
		attempt := s.restarts
		s.mu.Unlock()
		delay := restartDelay(policy, attempt)
		m.publish(s)
		m.mu.Unlock()

		m.log.Warn("session restarting", "channel", channelID, "attempt", attempt,
			"delay", delay.String(), "err", failure)
		select {
		case <-time.After(delay):
		case <-s.ctx.Done():
			return
		}

		if !m.isCurrentJob(channelID, s, failedJob) {
			return
		}
		started, err := m.launchDash(s)
		if err != nil {
			m.mu.Lock()
			if !m.currentJobLocked(channelID, s, failedJob) {
				m.mu.Unlock()
				return
			}
			s.mu.Lock()
			s.errors++
			s.lastError = err.Error()
			s.mu.Unlock()
			failure = err.Error()
			m.publish(s)
			m.mu.Unlock()
			continue
		}

		m.mu.Lock()
		if !m.currentJobLocked(channelID, s, failedJob) {
			m.mu.Unlock()
			cleanupDashStart(started)
			return
		}
		s.mu.RLock()
		oldWorkDir := s.workDir
		s.mu.RUnlock()
		s.install(started)
		m.publish(s)
		m.startWatcherLocked(channelID, s, started.job)
		m.mu.Unlock()

		cleanupJob(failedJob, oldWorkDir)
		return
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func restartDelay(policy RestartPolicy, attempt int) time.Duration {
	delay := policy.BaseDelay
	for i := 1; i < attempt && delay < policy.MaxDelay; i++ {
		if delay > policy.MaxDelay/2 {
			return policy.MaxDelay
		}
		delay *= 2
	}
	if delay > policy.MaxDelay {
		return policy.MaxDelay
	}
	return delay
}

func (m *Manager) currentJobLocked(channelID string, s *Session, job packager.Job) bool {
	if m.closing || m.sessions[channelID] != s {
		return false
	}
	s.mu.RLock()
	current := s.job == job
	s.mu.RUnlock()
	return current
}

func (m *Manager) isCurrentJob(channelID string, s *Session, job packager.Job) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.currentJobLocked(channelID, s, job)
}

func (m *Manager) touch(s *Session) {
	now := time.Now()
	s.mu.Lock()
	s.lastTouch = now
	channelID := s.Channel.ID
	s.mu.Unlock()
	if m.obs != nil {
		m.obs.TouchSession(channelID, now)
	}
}

type sessionCleanup struct {
	job     packager.Job
	workDir string
}

func (c sessionCleanup) run() {
	cleanupJob(c.job, c.workDir)
}

func (m *Manager) detachLocked(channelID string, s *Session, removeObservation bool) sessionCleanup {
	if m.sessions[channelID] == s {
		delete(m.sessions, channelID)
	}
	delete(m.viewers, channelID)
	s.cancel()
	s.mu.Lock()
	cleanup := sessionCleanup{job: s.job, workDir: s.workDir}
	s.job = nil
	s.publication = nil
	s.workDir = ""
	s.state = "stopped"
	s.mu.Unlock()
	if removeObservation {
		m.removePublished(channelID)
	}
	return cleanup
}

func (m *Manager) detachFailedLocked(channelID string, s *Session) sessionCleanup {
	if m.sessions[channelID] == s {
		delete(m.sessions, channelID)
	}
	s.cancel()
	s.mu.Lock()
	cleanup := sessionCleanup{job: s.job, workDir: s.workDir}
	s.job = nil
	s.publication = nil
	s.workDir = ""
	s.mu.Unlock()
	m.publish(s)
	return cleanup
}

func (m *Manager) removePublished(channelID string) {
	if m.obs != nil {
		m.obs.RemoveSession(channelID)
	}
}

func cleanupDashStart(started *dashStart) {
	if started != nil {
		cleanupJob(started.job, started.workDir)
	}
}

func cleanupJob(job packager.Job, workDir string) {
	if job != nil {
		_ = job.Stop()
	}
	if workDir != "" {
		_ = os.RemoveAll(workDir)
		_ = os.Remove(filepath.Dir(workDir))
	}
}

var generationFallback atomic.Uint64

func newGeneration() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return hex.EncodeToString(value[:])
	}
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), generationFallback.Add(1))
}

func (m *Manager) Touch(channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[channelID]; ok {
		m.touch(s)
	}
}

func (m *Manager) Get(channelID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[channelID]
	return s, ok
}

func (m *Manager) autostart(ctx context.Context) {
	var channels sync.WaitGroup
	for _, ch := range m.cat.ActiveChannels() {
		if !ch.Autostart {
			continue
		}
		channels.Add(1)
		go func(channelID string) {
			defer channels.Done()
			m.autostartChannel(ctx, channelID)
		}(ch.ID)
	}
	select {
	case <-ctx.Done():
	case <-m.lifecycleCtx.Done():
	}
	channels.Wait()
}

func (m *Manager) autostartChannel(ctx context.Context, channelID string) {
	attempt := 0
	for {
		if _, err := m.Acquire(channelID); err == nil {
			return
		} else if errors.Is(err, context.Canceled) {
			return
		} else {
			attempt++
			m.mu.Lock()
			policy := m.restart
			m.mu.Unlock()
			delay := restartDelay(policy, attempt)
			m.log.Error("autostart failed", "channel", channelID, "attempt", attempt,
				"retry_in", delay.String(), "err", err)
			if !m.waitForRetry(ctx, delay) {
				return
			}
		}
	}
}

func (m *Manager) waitForRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	case <-m.lifecycleCtx.Done():
		return false
	}
}

func (m *Manager) reaper(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-m.lifecycleCtx.Done():
			return
		case <-t.C:
			m.reapOnce()
			m.refreshPublished()
		}
	}
}

func (m *Manager) refreshPublished() {
	if m.obs == nil {
		return
	}
	m.mu.Lock()
	sessions := make(map[string]*Session, len(m.sessions))
	for id, s := range m.sessions {
		sessions[id] = s
	}
	m.mu.Unlock()

	for id, s := range sessions {
		stat := sessionStat(s)
		m.mu.Lock()
		if !m.closing && m.sessions[id] == s {
			m.obs.UpsertSession(stat)
		}
		m.mu.Unlock()
	}
}

func (m *Manager) reapOnce() {
	m.mu.Lock()
	now := time.Now()
	cleanups := make([]sessionCleanup, 0)
	for id, s := range m.sessions {
		channel, _, _, _ := s.SourceSnapshot()
		if !channel.OnDemand {
			continue
		}
		idle := m.cat.Config().IdleTimeout(channel)
		s.mu.RLock()
		lastTouch := s.lastTouch
		s.mu.RUnlock()
		if now.Sub(lastTouch) > idle {
			m.log.Info("session stopped", "channel", id, "reason", "idle")
			cleanups = append(cleanups, m.detachLocked(id, s, true))
		}
	}
	m.mu.Unlock()
	for _, cleanup := range cleanups {
		cleanup.run()
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	cleanups := make([]sessionCleanup, 0, len(m.sessions))
	for id, s := range m.sessions {
		cleanups = append(cleanups, m.detachLocked(id, s, true))
	}
	m.mu.Unlock()
	for _, cleanup := range cleanups {
		cleanup.run()
	}
}

func (m *Manager) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := m.ShutdownContext(ctx); err != nil {
		m.log.Error("session shutdown did not complete", "err", err)
	}
}

func (m *Manager) ShutdownContext(ctx context.Context) error {
	m.shutdownOnce.Do(m.beginShutdown)
	select {
	case <-m.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) beginShutdown() {
	m.lifecycleCancel()
	m.mu.Lock()
	m.closing = true
	waits := make([]*startWait, 0, len(m.inflight))
	for id, wait := range m.inflight {
		wait.stopped = true
		wait.cancel()
		delete(m.inflight, id)
		m.removePublished(id)
		waits = append(waits, wait)
	}
	cleanups := make([]sessionCleanup, 0, len(m.sessions))
	for id, s := range m.sessions {
		cleanups = append(cleanups, m.detachLocked(id, s, true))
	}
	m.mu.Unlock()

	var shutdownGroup sync.WaitGroup
	shutdownGroup.Add(len(waits) + len(cleanups) + 1)
	for _, wait := range waits {
		go func(wait *startWait) {
			defer shutdownGroup.Done()
			<-wait.done
		}(wait)
	}
	for _, cleanup := range cleanups {
		go func(cleanup sessionCleanup) {
			defer shutdownGroup.Done()
			cleanup.run()
		}(cleanup)
	}
	go func() {
		defer shutdownGroup.Done()
		m.watchers.Wait()
	}()
	go func() {
		shutdownGroup.Wait()
		close(m.shutdownDone)
	}()
}

func (m *Manager) StopChannel(channelID string) bool {
	m.mu.Lock()
	delete(m.viewers, channelID)
	s, ok := m.sessions[channelID]
	if ok {
		cleanup := m.detachLocked(channelID, s, true)
		m.mu.Unlock()
		cleanup.run()
		return true
	}
	w, ok := m.inflight[channelID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	w.stopped = true
	w.cancel()
	delete(m.inflight, channelID)
	m.removePublished(channelID)
	m.mu.Unlock()
	<-w.done
	return true
}

func (m *Manager) publish(s *Session) {
	if m.obs == nil {
		return
	}
	m.obs.UpsertSession(sessionStat(s))
}

func sessionStat(s *Session) observe.SessionStat {
	s.mu.RLock()
	stat := observe.SessionStat{
		ChannelID:      s.Channel.ID,
		Mode:           s.Mode,
		Engine:         s.engine,
		PackMode:       s.packMode,
		FallbackReason: s.fallbackReason,
		StartedAt:      s.StartedAt,
		LastTouch:      s.lastTouch,
		State:          s.state,
		Errors:         s.errors,
		LastError:      s.lastError,
	}
	job := s.job
	s.mu.RUnlock()
	stat.Packager = packagerStat(job)
	return stat
}

func packagerStat(job packager.Job) *observe.PackagerStat {
	if job == nil {
		return nil
	}
	st := job.Stats()
	if st == (packager.Stats{}) {
		return nil
	}
	return &observe.PackagerStat{
		SegmentsPublished:  st.SegmentsPublished,
		PartsPublished:     st.PartsPublished,
		SegmentsFetched:    st.SegmentsFetched,
		SegmentFetchErrs:   st.SegmentFetchErrs,
		ManifestRefreshes:  st.ManifestRefreshes,
		ManifestErrs:       st.ManifestErrs,
		Discontinuities:    st.Discontinuities,
		Reanchors:          st.Reanchors,
		Reresolves:         st.Reresolves,
		TrackHolds:         st.TrackHolds,
		KeyMismatches:      st.KeyMismatches,
		DecryptSeconds:     st.DecryptSeconds,
		CacheBytes:         st.CacheBytes,
		CacheItems:         st.CacheItems,
		VideoFrontier:      st.VideoFrontier,
		AudioFrontier:      st.AudioFrontier,
		VideoTracks:        st.VideoTracks,
		AudioTracks:        st.AudioTracks,
		TextTracks:         st.TextTracks,
		ClockOffsetSeconds: st.ClockOffsetSeconds,
	}
}

func (m *Manager) HeadersFor(ch config.Channel) map[string]string {
	up, err := m.cat.Upstream(ch)
	if err != nil {
		return ch.Headers
	}
	out := map[string]string{}
	for k, v := range up.Headers {
		out[k] = v
	}
	for k, v := range ch.Headers {
		out[k] = v
	}
	return out
}
