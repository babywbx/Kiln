package session_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/session"
)

func TestManagerRetriesRestartStartFailuresUntilBudgetIsExhausted(t *testing.T) {
	t.Parallel()

	first := newFakeJob()
	engine := &fakePackager{results: []startResult{
		{job: first},
		{err: errors.New("restart one failed")},
		{err: errors.New("restart two failed")},
		{err: errors.New("restart three failed")},
	}}
	manager, obs := newRestartManager(t, engine)
	manager.SetRestartPolicy(session.RestartPolicy{
		MaxAttempts: 3,
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Millisecond,
		ResetAfter:  time.Hour,
	})
	defer manager.Shutdown()

	if _, err := manager.Acquire("news"); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	first.fail(errors.New("upstream disconnected"))

	eventually(t, func() bool { return engine.startCount() == 4 })
	eventually(t, func() bool { return sessionState(obs, "news") == "failed" })
	if _, ok := manager.Get("news"); ok {
		t.Fatal("Get() found a terminally failed session")
	}
	if got := engine.startCount(); got != 4 {
		t.Fatalf("packager starts = %d, want initial start plus 3 retries", got)
	}
}

func TestManagerTouchDoesNotOverwriteRestartingState(t *testing.T) {
	t.Parallel()

	first := newFakeJob()
	second := newFakeJob()
	engine := &fakePackager{results: []startResult{{job: first}, {job: second}}}
	manager, obs := newRestartManager(t, engine)
	manager.SetRestartPolicy(session.RestartPolicy{
		MaxAttempts: 1,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    100 * time.Millisecond,
		ResetAfter:  time.Hour,
	})
	defer manager.Shutdown()

	initial, err := manager.Acquire("news")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	first.fail(errors.New("upstream disconnected"))
	eventually(t, func() bool { return sessionState(obs, "news") == "restarting" })

	manager.Touch("news")
	acquired, err := manager.Acquire("news")
	if err != nil {
		t.Fatalf("Acquire() while restarting error = %v", err)
	}
	if acquired != initial {
		t.Fatal("Acquire() replaced the restarting session")
	}
	if got := sessionState(obs, "news"); got != "restarting" {
		t.Fatalf("state after Touch()/Acquire() = %q, want restarting", got)
	}
}

func TestStoppedBackoffCannotReplaceNewSessionForSameChannel(t *testing.T) {
	t.Parallel()

	first := newFakeJob()
	second := newFakeJob()
	engine := &fakePackager{results: []startResult{{job: first}, {job: second}}}
	manager, _ := newRestartManager(t, engine)
	manager.SetRestartPolicy(session.RestartPolicy{
		MaxAttempts: 2,
		BaseDelay:   80 * time.Millisecond,
		MaxDelay:    80 * time.Millisecond,
		ResetAfter:  time.Hour,
	})
	defer manager.Shutdown()

	oldSession, err := manager.Acquire("news")
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	first.fail(errors.New("upstream disconnected"))
	eventually(t, func() bool {
		return oldSession.State() == "restarting"
	})
	if !manager.StopChannel("news") {
		t.Fatal("StopChannel() = false")
	}
	newSession, err := manager.Acquire("news")
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if newSession == oldSession {
		t.Fatal("Acquire() reused the stopped session")
	}
	_, generation := newSession.PublicationSnapshot()

	time.Sleep(160 * time.Millisecond)
	current, ok := manager.Get("news")
	if !ok || current != newSession {
		t.Fatal("old watcher replaced or removed the new session")
	}
	_, currentGeneration := current.PublicationSnapshot()
	if currentGeneration != generation {
		t.Fatalf("generation changed from %q to %q", generation, currentGeneration)
	}
	if got := engine.startCount(); got != 2 {
		t.Fatalf("packager starts = %d, want exactly 2", got)
	}
}

func TestRestartKeepsPublicationUntilReplacementIsReady(t *testing.T) {
	t.Parallel()

	first := newFakeJob()
	second := newFakeJob()
	restartStarted := make(chan struct{})
	releaseRestart := make(chan struct{})
	engine := &fakePackager{results: []startResult{
		{job: first},
		{start: func(ctx context.Context, _ packager.Request) (packager.Job, error) {
			close(restartStarted)
			select {
			case <-releaseRestart:
				return second, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}},
	}}
	manager, _ := newRestartManager(t, engine)
	manager.SetRestartPolicy(session.RestartPolicy{
		MaxAttempts: 1,
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Millisecond,
		ResetAfter:  time.Hour,
	})
	defer manager.Shutdown()

	sess, err := manager.Acquire("news")
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	publication, generation := sess.PublicationSnapshot()
	first.fail(errors.New("upstream disconnected"))
	select {
	case <-restartStarted:
	case <-time.After(time.Second):
		t.Fatal("restart did not start")
	}
	if got, gotGeneration := sess.PublicationSnapshot(); got != publication || gotGeneration != generation {
		t.Fatal("current publication changed before replacement became ready")
	}
	close(releaseRestart)
	eventually(t, func() bool {
		got, gotGeneration := sess.PublicationSnapshot()
		return got == second.Publication() && gotGeneration != generation
	})

	requests := engine.requestsSnapshot()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	if requests[0].WorkDir == requests[1].WorkDir {
		t.Fatalf("restart reused workdir %q", requests[0].WorkDir)
	}
	if filepath.Dir(requests[0].WorkDir) != filepath.Dir(requests[1].WorkDir) {
		t.Fatalf("workdirs do not share channel root: %q and %q", requests[0].WorkDir, requests[1].WorkDir)
	}
}

func TestAutostartRetriesChannelsIndependently(t *testing.T) {
	t.Parallel()

	engine := &autostartPackager{badFailures: 2, calls: map[string]int{}, jobs: map[string]*fakeJob{}}
	channel := func(id string) config.Channel {
		return config.Channel{
			ID:        id,
			Upstream:  "origin",
			Path:      "/live.mpd",
			Ingress:   "dash",
			Keys:      "00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100",
			Autostart: true,
		}
	}
	cfg := config.File{
		Upstreams: []config.Upstream{{ID: "origin", BaseURL: "https://example.com"}},
		Channels:  []config.Channel{channel("bad"), channel("good")},
	}
	manager := session.NewManager(
		catalog.New(cfg, nil),
		nil,
		observe.New(),
		t.TempDir(),
		config.FFmpeg{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)
	manager.SetPackager(engine)
	manager.SetRestartPolicy(session.RestartPolicy{
		BaseDelay:  time.Millisecond,
		MaxDelay:   time.Millisecond,
		ResetAfter: time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)
	defer manager.Shutdown()

	eventually(t, func() bool {
		_, good := manager.Get("good")
		return good && engine.startCount("good") == 1
	})
	eventually(t, func() bool {
		_, bad := manager.Get("bad")
		return bad && engine.startCount("bad") == 3
	})
}

func TestShutdownContextBoundsUnresponsivePackagerStop(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	job := &blockingStopJob{fakeJob: newFakeJob(), release: release}
	manager, _ := newRestartManager(t, &fakePackager{results: []startResult{{job: job}}})
	if _, err := manager.Acquire("news"); err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := manager.ShutdownContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownContext() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("ShutdownContext() took %s", elapsed)
	}

	close(release)
	manager.Shutdown()
}

func TestShutdownCleansActiveSessionWhileAnotherStartIsBlocked(t *testing.T) {
	t.Parallel()

	stopStarted := make(chan struct{})
	releaseStop := make(chan struct{})
	startBlocked := make(chan struct{})
	releaseStart := make(chan struct{})
	active := &notifyingBlockingStopJob{
		fakeJob: newFakeJob(),
		started: stopStarted,
		release: releaseStop,
	}
	engine := &shutdownPackager{
		active:       active,
		startBlocked: startBlocked,
		releaseStart: releaseStart,
	}
	channel := func(id string) config.Channel {
		return config.Channel{
			ID:               id,
			Upstream:         "origin",
			Path:             "/live.mpd",
			Ingress:          "dash",
			Keys:             "00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100",
			RestartOnFailure: true,
		}
	}
	manager := session.NewManager(
		catalog.New(config.File{
			Upstreams: []config.Upstream{{ID: "origin", BaseURL: "https://example.com"}},
			Channels:  []config.Channel{channel("active"), channel("starting")},
		}, nil),
		nil,
		observe.New(),
		t.TempDir(),
		config.FFmpeg{},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)
	manager.SetPackager(engine)
	if _, err := manager.Acquire("active"); err != nil {
		t.Fatalf("Acquire(active) error = %v", err)
	}
	if err := manager.Warmup("starting"); err != nil {
		t.Fatalf("Warmup(starting) error = %v", err)
	}
	select {
	case <-startBlocked:
	case <-time.After(time.Second):
		t.Fatal("second packager start did not block")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.ShutdownContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownContext() error = %v, want deadline exceeded", err)
	}
	select {
	case <-stopStarted:
	default:
		t.Fatal("active packager cleanup waited behind the blocked start")
	}

	close(releaseStop)
	close(releaseStart)
	manager.Shutdown()
}

func newRestartManager(t *testing.T, engine packager.Packager) (*session.Manager, *observe.Service) {
	t.Helper()
	manager, obs := newManager(t, config.Channel{
		ID:               "news",
		Upstream:         "origin",
		Path:             "/live.mpd",
		Ingress:          "dash",
		Keys:             "00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100",
		RestartOnFailure: true,
	})
	manager.SetPackager(engine)
	return manager, obs
}

type startResult struct {
	job   packager.Job
	err   error
	start func(context.Context, packager.Request) (packager.Job, error)
}

type fakePackager struct {
	mu       sync.Mutex
	results  []startResult
	requests []packager.Request
}

func (p *fakePackager) Start(ctx context.Context, req packager.Request) (packager.Job, error) {
	p.mu.Lock()
	p.requests = append(p.requests, req)
	if len(p.results) == 0 {
		p.mu.Unlock()
		return nil, errors.New("unexpected packager start")
	}
	result := p.results[0]
	p.results = p.results[1:]
	p.mu.Unlock()
	if result.start != nil {
		return result.start(ctx, req)
	}
	return result.job, result.err
}

func (p *fakePackager) startCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.requests)
}

func (p *fakePackager) requestsSnapshot() []packager.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]packager.Request(nil), p.requests...)
}

type fakeJob struct {
	publication packager.Publication
	done        chan struct{}
	doneOnce    sync.Once
	intentional atomic.Bool
	errMu       sync.Mutex
	err         error
}

type blockingStopJob struct {
	*fakeJob
	release <-chan struct{}
}

func (j *blockingStopJob) Stop() error {
	<-j.release
	return j.fakeJob.Stop()
}

type notifyingBlockingStopJob struct {
	*fakeJob
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (j *notifyingBlockingStopJob) Stop() error {
	j.once.Do(func() { close(j.started) })
	<-j.release
	return j.fakeJob.Stop()
}

type shutdownPackager struct {
	active       packager.Job
	startBlocked chan struct{}
	releaseStart <-chan struct{}
	once         sync.Once
}

func (p *shutdownPackager) Start(_ context.Context, req packager.Request) (packager.Job, error) {
	if req.ChannelID == "active" {
		return p.active, nil
	}
	p.once.Do(func() { close(p.startBlocked) })
	<-p.releaseStart
	return newFakeJob(), nil
}

func newFakeJob() *fakeJob {
	return &fakeJob{publication: fakePublication{}, done: make(chan struct{})}
}

func (j *fakeJob) Publication() packager.Publication { return j.publication }
func (j *fakeJob) Engine() string                    { return packager.EngineNativeRewrite }
func (j *fakeJob) PackMode() string                  { return "dynamic_timeline" }
func (j *fakeJob) FallbackReason() string            { return "" }
func (j *fakeJob) Done() <-chan struct{}             { return j.done }
func (j *fakeJob) IntentionalStop() bool             { return j.intentional.Load() }
func (j *fakeJob) Stats() packager.Stats             { return packager.Stats{} }

func (j *fakeJob) Err() error {
	j.errMu.Lock()
	defer j.errMu.Unlock()
	return j.err
}

func (j *fakeJob) Stop() error {
	j.intentional.Store(true)
	j.doneOnce.Do(func() { close(j.done) })
	return nil
}

func (j *fakeJob) fail(err error) {
	j.errMu.Lock()
	j.err = err
	j.errMu.Unlock()
	j.doneOnce.Do(func() { close(j.done) })
}

type fakePublication struct{}

func (fakePublication) Master() string                      { return "master.m3u8" }
func (fakePublication) Playlist(string) ([]byte, bool)      { return []byte("#EXTM3U\n"), true }
func (fakePublication) Asset(string) (packager.Asset, bool) { return packager.Asset{}, false }

type autostartPackager struct {
	mu          sync.Mutex
	badFailures int
	calls       map[string]int
	jobs        map[string]*fakeJob
}

func (p *autostartPackager) Start(_ context.Context, req packager.Request) (packager.Job, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[req.ChannelID]++
	if req.ChannelID == "bad" && p.calls[req.ChannelID] <= p.badFailures {
		return nil, errors.New("upstream unavailable")
	}
	job := newFakeJob()
	p.jobs[req.ChannelID] = job
	return job, nil
}

func (p *autostartPackager) startCount(channelID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[channelID]
}
