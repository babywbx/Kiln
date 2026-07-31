package session_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/session"
)

func TestManagerWarmupStartsHLSChannel(t *testing.T) {
	t.Parallel()

	manager, _ := newManager(t, config.Channel{
		ID:       "news",
		Upstream: "origin",
		Path:     "/live.m3u8",
		Ingress:  "hls",
	})

	if err := manager.Warmup("news"); err != nil {
		t.Fatalf("Warmup() error = %v", err)
	}

	eventually(t, func() bool {
		_, ok := manager.Get("news")
		return ok
	})
}

func TestManagerWarmupPublishesStarting(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var startedOnce sync.Once
	var canceledOnce sync.Once
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		startedOnce.Do(func() { close(requestStarted) })
		select {
		case <-r.Context().Done():
			canceledOnce.Do(func() { close(requestCanceled) })
		case <-releaseUpstream:
		}
	}))
	defer func() {
		close(releaseUpstream)
		upstream.Close()
	}()

	manager, obs := newManagerWithBaseURL(t, upstream.URL, config.Channel{
		ID:       "news",
		Upstream: "origin",
		Path:     "/live.mpd",
		Ingress:  "dash",
	})
	defer manager.StopChannel("news")

	if err := manager.Warmup("news"); err != nil {
		t.Fatalf("Warmup() error = %v", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("warmup did not request the upstream")
	}

	eventually(t, func() bool {
		return sessionState(obs, "news") == "starting"
	})
	if err := manager.Warmup("news"); err != nil {
		t.Fatalf("second Warmup() error = %v", err)
	}
	if stopped := manager.StopChannel("news"); !stopped {
		t.Fatal("StopChannel() = false while channel is starting")
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("StopChannel() did not cancel the upstream request")
	}
	eventually(t, func() bool {
		return sessionState(obs, "news") == ""
	})
	time.Sleep(10 * time.Millisecond)
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}
	if _, ok := manager.Get("news"); ok {
		t.Fatal("Get() found session after stopping warmup")
	}
}

func TestManagerShutdownCancelsWarmup(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	requestCanceled := make(chan struct{})
	releaseUpstream := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		select {
		case <-r.Context().Done():
			close(requestCanceled)
		case <-releaseUpstream:
		}
	}))
	defer func() {
		close(releaseUpstream)
		upstream.Close()
	}()

	manager, _ := newManagerWithBaseURL(t, upstream.URL, config.Channel{
		ID:       "news",
		Upstream: "origin",
		Path:     "/live.mpd",
		Ingress:  "dash",
	})

	if err := manager.Warmup("news"); err != nil {
		t.Fatalf("Warmup() error = %v", err)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("warmup did not request the upstream")
	}
	manager.Shutdown()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("Shutdown() did not cancel the inflight warmup")
	}
}

func TestManagerRejectsStartsAfterShutdown(t *testing.T) {
	t.Parallel()

	manager, _ := newManager(t, config.Channel{
		ID:       "news",
		Upstream: "origin",
		Path:     "/live.m3u8",
		Ingress:  "hls",
	})
	manager.Shutdown()
	if err := manager.Warmup("news"); err == nil {
		t.Fatal("Warmup() succeeded after Shutdown()")
	}
	if _, err := manager.Acquire("news"); err == nil {
		t.Fatal("Acquire() succeeded after Shutdown()")
	}
}

func TestManagerWarmupPublishesFailure(t *testing.T) {
	t.Parallel()

	manager, obs := newManagerWithBaseURLAndKeys(t, "https://example.com", config.Channel{
		ID:       "news",
		Upstream: "origin",
		Path:     "/live.mpd",
		Ingress:  "dash",
	}, nil)

	if err := manager.Warmup("news"); err != nil {
		t.Fatalf("Warmup() error = %v", err)
	}

	eventually(t, func() bool {
		stat, ok := findSession(obs, "news")
		return ok && stat.State == "failed" && stat.LastError != ""
	})
	if _, ok := manager.Get("news"); ok {
		t.Fatal("Get() found session after failed warmup")
	}
}

func TestManagerWarmupRejectsUnknownChannel(t *testing.T) {
	t.Parallel()

	manager, _ := newManager(t, config.Channel{
		ID:       "news",
		Upstream: "origin",
		Path:     "/live.m3u8",
		Ingress:  "hls",
	})

	if err := manager.Warmup("missing"); err != session.ErrNotFound {
		t.Fatalf("Warmup() error = %v, want ErrNotFound", err)
	}
}

func TestManagerEnforcesMaximumActiveViewers(t *testing.T) {
	t.Parallel()

	manager, _ := newManager(t, config.Channel{
		ID:         "news",
		Upstream:   "origin",
		Path:       "/live.m3u8",
		Ingress:    "hls",
		MaxViewers: 1,
	})

	if err := manager.RefreshViewer("news", "viewer-a"); err != session.ErrViewerLease {
		t.Fatalf("RefreshViewer(before admit) error = %v, want ErrViewerLease", err)
	}
	if err := manager.AdmitViewer("news", "viewer-a"); err != nil {
		t.Fatalf("AdmitViewer(first) error = %v", err)
	}
	if err := manager.RefreshViewer("news", "viewer-a"); err != nil {
		t.Fatalf("RefreshViewer(active) error = %v", err)
	}
	if err := manager.AdmitViewer("news", "viewer-b"); err != session.ErrViewerLimit {
		t.Fatalf("AdmitViewer(second) error = %v, want ErrViewerLimit", err)
	}
}

func newManager(t *testing.T, channel config.Channel) (*session.Manager, *observe.Service) {
	t.Helper()
	return newManagerWithBaseURL(t, "https://example.com", channel)
}

func newManagerWithBaseURL(t *testing.T, baseURL string, channel config.Channel) (*session.Manager, *observe.Service) {
	t.Helper()
	return newManagerWithBaseURLAndKeys(t, baseURL, channel, testKeyPairs())
}

func newManagerWithBaseURLAndKeys(t *testing.T, baseURL string, channel config.Channel, keys []config.KeyPair) (*session.Manager, *observe.Service) {
	t.Helper()

	cfg := config.File{
		Upstreams: []config.Upstream{{
			ID:      "origin",
			BaseURL: baseURL,
		}},
		Channels: []config.Channel{channel},
	}
	obs := observe.New()
	allowed := map[string]struct{}{}
	if upstreamURL, err := url.Parse(baseURL); err == nil {
		allowed[upstreamURL.Hostname()] = struct{}{}
	}
	puller := pull.New(pull.Options{Observe: obs, Allowed: allowed})
	manager := session.NewManager(
		catalog.New(cfg, nil),
		puller,
		obs,
		t.TempDir(),
		config.FFmpeg{},
		keys,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
	)
	return manager, obs
}

func testKeyPairs() []config.KeyPair {
	return []config.KeyPair{{
		KID: "00112233445566778899aabbccddeeff",
		Key: "ffeeddccbbaa99887766554433221100",
	}}
}

func sessionState(obs *observe.Service, channelID string) string {
	stat, ok := findSession(obs, channelID)
	if !ok {
		return ""
	}
	return stat.State
}

func findSession(obs *observe.Service, channelID string) (observe.SessionStat, bool) {
	for _, stat := range obs.Snapshot().Sessions {
		if stat.ChannelID == channelID {
			return stat, true
		}
	}
	return observe.SessionStat{}, false
}

func eventually(t *testing.T, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met before timeout")
}
