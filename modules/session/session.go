package session

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
)

var ErrNotFound = apperr.New(apperr.CodeNotFound, 404, "channel not found")

const (
	maxDashRestarts   = 8
	restartBaseDelay  = 2 * time.Second
	restartMaxDelay   = 30 * time.Second
	restartResetAfter = 90 * time.Second
)

// spawnGate bounds concurrent packager launches. It is deliberately scoped to
// the launch itself: the previous global semaphore was held across the whole
// readiness wait, so one slow source blocked every other channel's cold start
// for minutes.
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
	cat     *catalog.Service
	pull    *pull.Client
	obs     *observe.Service
	egress  *proxyegress.Router
	dataDir string
	ffmpeg  config.FFmpeg
	log     *slog.Logger
	spawn   spawnGate
	pack    packager.Packager

	mu       sync.Mutex
	sessions map[string]*Session
	inflight map[string]*startWait
	closing  bool
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
	LastTouch time.Time
	WorkDir   string
	Errors    int
	LastError string
	// Engine is the resolved engine; PackMode is that engine's internal mode.
	// They are separate axes: reusing PackMode for both would silently change
	// the values existing status API consumers already see.
	Engine         string
	PackMode       string
	FallbackReason string

	job      packager.Job
	cancel   context.CancelFunc
	ctx      context.Context
	restarts int
	lastOK   time.Time
}

// Publication exposes the published media. The HTTP layer goes through this
// and never guesses the on-disk layout.
func (s *Session) Publication() packager.Publication {
	if s.job == nil {
		return nil
	}
	return s.job.Publication()
}

func NewManager(cat *catalog.Service, pullClient *pull.Client, obs *observe.Service, dataDir string, ff config.FFmpeg, log *slog.Logger, egress *proxyegress.Router) *Manager {
	if log == nil {
		log = slog.Default()
	}
	m := &Manager{
		cat:      cat,
		pull:     pullClient,
		obs:      obs,
		egress:   egress,
		dataDir:  dataDir,
		ffmpeg:   ff,
		log:      log,
		spawn:    newSpawnGate(ff.MaxStarts),
		sessions: map[string]*Session{},
		inflight: map[string]*startWait{},
	}
	m.pack = m.newPackager()
	return m
}

func (m *Manager) newPackager() packager.Packager {
	cfg := m.cat.Config().Packager
	onBytesIn := func(n int64) {
		if m.obs != nil {
			m.obs.AddBytesIn(n)
		}
	}
	native := packager.NewNativeAdapter(
		packager.NewPullFetcher(m.pull, cfg.MaxSegmentBytes),
		cfg.PlaylistSize,
		time.Duration(cfg.GraceSec)*time.Second,
	)
	native.StartSegments = cfg.StartSegments
	native.Prefetch = cfg.PrefetchSegments
	native.MaxSegmentBytes = cfg.MaxSegmentBytes
	native.PrimaryTrackHold = time.Duration(cfg.PrimaryTrackHoldSec) * time.Second
	native.SetInflightBytes(cfg.InflightBytes)
	ffmpeg := packager.NewFFmpegAdapter(m.ffmpeg, m.egress, m.spawn, onBytesIn)
	return packager.NewAdaptivePackager(native, ffmpeg, m.log)
}

// SetPackager replaces the engine, for tests that must not launch ffmpeg.
func (m *Manager) SetPackager(p packager.Packager) { m.pack = p }

func (m *Manager) Start(ctx context.Context) {
	go m.reaper(ctx)
	go m.autostart(ctx)
}

func (m *Manager) Pull() *pull.Client { return m.pull }

// Warmup starts a channel asynchronously and is idempotent while it is active.
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
		s.LastTouch = time.Now()
		m.publish(s, "running")
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
			s.LastTouch = time.Now()
			m.publish(s, "running")
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
					cur.LastTouch = time.Now()
					m.publish(cur, "running")
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
		Channel:   ch,
		SourceURL: src,
		Upstream:  up,
		Mode:      ch.Ingress,
		StartedAt: now,
		LastTouch: now,
		cancel:    cancel,
		ctx:       sctx,
	}
	w := &startWait{done: make(chan struct{}), sess: s, cancel: cancel}
	m.inflight[ch.ID] = w
	m.publish(s, "starting")
	return w, s
}

func (m *Manager) finishStart(channelID string, w *startWait, s *Session) (*Session, error) {
	var startErr error
	if s.Channel.Ingress == "dash" {
		startErr = m.startDash(s)
	}

	m.mu.Lock()
	current := m.inflight[channelID] == w
	if current {
		delete(m.inflight, channelID)
	}
	if w.stopped || !current {
		if s.job != nil {
			_ = s.job.Stop()
		}
		w.cancel()
		w.err = context.Canceled
		close(w.done)
		m.mu.Unlock()
		return nil, context.Canceled
	}
	if startErr != nil {
		w.cancel()
		s.LastError = startErr.Error()
		m.publish(s, "failed")
		w.err = startErr
		close(w.done)
		m.mu.Unlock()
		return nil, startErr
	}
	if existing, ok := m.sessions[channelID]; ok {
		if s.job != nil {
			_ = s.job.Stop()
		}
		w.cancel()
		existing.LastTouch = time.Now()
		w.sess = existing
		close(w.done)
		m.mu.Unlock()
		return existing, nil
	}
	s.lastOK = time.Now()
	m.sessions[channelID] = s
	m.publish(s, "running")
	close(w.done)
	m.mu.Unlock()
	m.log.Info("session started", "channel", channelID, "ingress", s.Channel.Ingress,
		"engine", s.Engine, "pack_mode", s.PackMode, "fallback_reason", s.FallbackReason)
	return s, nil
}

// channelKeys prefers the keys typed into the channel over a file on disk. A
// deployed server often has no path an operator can reach to drop a file at.
func channelKeys(ch config.Channel) ([]config.KeyPair, error) {
	if ch.Keys != "" {
		return config.ParseKeys(ch.Keys)
	}
	return config.LoadKeysFile(ch.KeysFile)
}

func (m *Manager) startDash(s *Session) error {
	keys, err := channelKeys(s.Channel)
	if err != nil {
		return apperr.Wrap(apperr.CodeInvalid, 400, "load keys failed", err)
	}
	work := filepath.Join(m.dataDir, "sessions", s.Channel.ID)
	if s.job != nil {
		_ = s.job.Stop()
		s.job = nil
	}
	_ = os.RemoveAll(work)
	headers := maps.Clone(s.Upstream.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	maps.Copy(headers, s.Channel.Headers)

	prefer := m.ffmpeg.PreferHeight
	if s.Channel.PreferHeight > 0 {
		prefer = s.Channel.PreferHeight
	}
	job, err := m.pack.Start(s.ctx, packager.Request{
		ChannelID:    s.Channel.ID,
		SourceURL:    s.SourceURL,
		Keys:         keys,
		Headers:      headers,
		UserAgent:    s.Channel.UserAgent,
		WorkDir:      work,
		PreferHeight: prefer,
		Engine:       m.cat.Config().EngineFor(s.Channel),
		Log:          m.log.With("channel", s.Channel.ID),
	})
	if err != nil {
		s.LastError = err.Error()
		return apperr.Wrap(apperr.CodeUpstream, 502, "dash packager failed", err)
	}
	s.job = job
	s.WorkDir = work
	s.Engine = job.Engine()
	s.PackMode = job.PackMode()
	s.FallbackReason = job.FallbackReason()
	s.LastError = ""
	s.lastOK = time.Now()
	if s.Channel.RestartOnFailure {
		go m.watchJob(s.Channel.ID, job)
	}
	return nil
}

// watchJob owns restart policy for both engines. The adapters do not each
// reimplement the restart budget, idle rules or backoff.
func (m *Manager) watchJob(channelID string, job packager.Job) {
	<-job.Done()
	if job.IntentionalStop() {
		return
	}

	m.mu.Lock()
	s, ok := m.sessions[channelID]
	if !ok || s.job != job {
		m.mu.Unlock()
		return
	}
	if s.Channel.OnDemand {
		idle := m.cat.Config().IdleTimeout(s.Channel)
		if time.Since(s.LastTouch) > idle {
			m.log.Info("session idle end", "channel", channelID)
			m.stopLocked(channelID, s)
			m.mu.Unlock()
			return
		}
	}
	if !s.lastOK.IsZero() && time.Since(s.lastOK) >= restartResetAfter {
		s.restarts = 0
	}
	s.Errors++
	s.restarts++
	s.LastError = errString(job.Err())
	if s.restarts > maxDashRestarts {
		m.log.Error("session restart budget exceeded", "channel", channelID, "restarts", s.restarts, "err", job.Err())
		m.publish(s, "failed")
		m.stopLocked(channelID, s)
		m.mu.Unlock()
		return
	}
	delay := restartBaseDelay * time.Duration(1<<min(s.restarts-1, 4))
	if delay > restartMaxDelay {
		delay = restartMaxDelay
	}
	m.publish(s, "restarting")
	m.mu.Unlock()

	m.log.Warn("session restarting",
		"channel", channelID,
		"attempt", s.restarts,
		"delay", delay.String(),
		"err", job.Err(),
	)
	time.Sleep(delay)

	m.mu.Lock()
	s, ok = m.sessions[channelID]
	if !ok {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	if err := m.startDash(s); err != nil {
		m.log.Error("session restart failed", "channel", channelID, "err", err)
		m.mu.Lock()
		if cur, ok := m.sessions[channelID]; ok && cur == s {
			s.LastError = err.Error()
			m.publish(s, "failed")
			m.stopLocked(channelID, cur)
		}
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	if cur, ok := m.sessions[channelID]; !ok || cur != s {
		if s.job != nil {
			_ = s.job.Stop()
		}
		m.mu.Unlock()
		return
	}
	m.publish(s, "running")
	m.mu.Unlock()
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *Manager) Touch(channelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[channelID]; ok {
		s.LastTouch = time.Now()
		m.publish(s, "running")
	}
}

func (m *Manager) Get(channelID string) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[channelID]
	return s, ok
}

func (m *Manager) autostart(ctx context.Context) {
	for _, ch := range m.cat.Config().ActiveChannels() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !ch.Autostart {
			continue
		}
		if _, err := m.Acquire(ch.ID); err != nil {
			m.log.Error("autostart failed", "channel", ch.ID, "err", err)
		}
	}
	<-ctx.Done()
}

func (m *Manager) reaper(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			return
		case <-t.C:
			m.reapOnce()
		}
	}
}

func (m *Manager) reapOnce() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for id, s := range m.sessions {
		if !s.Channel.OnDemand {
			continue
		}
		idle := m.cat.Config().IdleTimeout(s.Channel)
		if now.Sub(s.LastTouch) > idle {
			m.log.Info("session stopped", "channel", id, "reason", "idle")
			m.stopLocked(id, s)
		}
	}
}

func (m *Manager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		m.stopLocked(id, s)
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.closing = true
	waits := make([]*startWait, 0, len(m.inflight))
	for id, wait := range m.inflight {
		wait.stopped = true
		wait.cancel()
		delete(m.inflight, id)
		m.obs.RemoveSession(id)
		waits = append(waits, wait)
	}
	m.mu.Unlock()
	for _, wait := range waits {
		<-wait.done
	}
	m.stopAll()
}

func (m *Manager) stopLocked(id string, s *Session) {
	if s.job != nil {
		_ = s.job.Stop()
		s.job = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.WorkDir != "" {
		_ = os.RemoveAll(s.WorkDir)
	}
	delete(m.sessions, id)
	m.obs.RemoveSession(id)
}

func (m *Manager) StopChannel(channelID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[channelID]
	if ok {
		m.stopLocked(channelID, s)
		return true
	}
	w, ok := m.inflight[channelID]
	if !ok {
		return false
	}
	w.stopped = true
	w.cancel()
	delete(m.inflight, channelID)
	m.obs.RemoveSession(channelID)
	return true
}

func (m *Manager) publish(s *Session, state string) {
	m.obs.UpsertSession(observe.SessionStat{
		ChannelID:      s.Channel.ID,
		Mode:           s.Mode,
		Engine:         s.Engine,
		PackMode:       s.PackMode,
		FallbackReason: s.FallbackReason,
		StartedAt:      s.StartedAt,
		LastTouch:      s.LastTouch,
		State:          state,
		Errors:         s.Errors,
		LastError:      s.LastError,
		Packager:       packagerStat(s.job),
	})
}

// packagerStat copies the engine's counters into the status snapshot. An engine
// that reports nothing stays absent instead of showing a row of zeros.
func packagerStat(job packager.Job) *observe.PackagerStat {
	if job == nil {
		return nil
	}
	st := job.Stats()
	if st == (packager.Stats{}) {
		return nil
	}
	return &observe.PackagerStat{
		SegmentsPublished: st.SegmentsPublished,
		SegmentsFetched:   st.SegmentsFetched,
		SegmentFetchErrs:  st.SegmentFetchErrs,
		ManifestRefreshes: st.ManifestRefreshes,
		ManifestErrs:      st.ManifestErrs,
		Discontinuities:   st.Discontinuities,
		Reanchors:         st.Reanchors,
		TrackHolds:        st.TrackHolds,
		KeyMismatches:     st.KeyMismatches,
		DecryptSeconds:    st.DecryptSeconds,
		CacheBytes:        st.CacheBytes,
		CacheItems:        st.CacheItems,
		VideoFrontier:     st.VideoFrontier,
		AudioFrontier:     st.AudioFrontier,
		AudioTracks:       st.AudioTracks,
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
