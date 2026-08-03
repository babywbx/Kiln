package session

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

type staticCatalog struct {
	channels map[string]config.Channel
}

func (c staticCatalog) Config() config.File { return config.File{} }

func (c staticCatalog) Get(id string) (config.Channel, bool) {
	ch, ok := c.channels[id]
	return ch, ok
}

func (c staticCatalog) SourceURL(config.Channel) (string, error) {
	return "https://example.com/live.m3u8", nil
}

func (c staticCatalog) Upstream(config.Channel) (config.Upstream, error) {
	return config.Upstream{}, nil
}

func newInternalManager(t *testing.T, channels ...config.Channel) *Manager {
	t.Helper()
	byID := make(map[string]config.Channel, len(channels))
	for _, ch := range channels {
		byID[ch.ID] = ch
	}
	return newManager(
		staticCatalog{channels: byID}, nil, nil, t.TempDir(), config.FFmpeg{}, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), nil,
	)
}

func TestExpiredViewerLeaseFreesCapacity(t *testing.T) {
	t.Parallel()

	m := newInternalManager(t, config.Channel{ID: "news", Ingress: "hls", OnDemand: true, MaxViewers: 1})

	if err := m.AdmitViewer("news", "viewer-a"); err != nil {
		t.Fatalf("AdmitViewer(viewer-a) error = %v", err)
	}
	if err := m.AdmitViewer("news", "viewer-b"); err != ErrViewerLimit {
		t.Fatalf("AdmitViewer(viewer-b) error = %v, want ErrViewerLimit", err)
	}

	m.mu.Lock()
	m.viewers["news"]["viewer-a"] = time.Now().Add(-viewerLeaseTTL - time.Second)
	m.mu.Unlock()

	if err := m.AdmitViewer("news", "viewer-b"); err != nil {
		t.Fatalf("AdmitViewer(after expiry) error = %v", err)
	}
	if err := m.RefreshViewer("news", "viewer-a"); err != ErrViewerLease {
		t.Fatalf("RefreshViewer(expired) error = %v, want ErrViewerLease", err)
	}
	if err := m.RefreshViewer("news", "viewer-b"); err != nil {
		t.Fatalf("RefreshViewer(active) error = %v", err)
	}
}

func TestReapOnceStopsOnlyIdleOnDemandSessions(t *testing.T) {
	t.Parallel()

	m := newInternalManager(t,
		config.Channel{ID: "idle", Ingress: "hls", OnDemand: true, MaxViewers: 2},
		config.Channel{ID: "pinned", Ingress: "hls"},
	)

	idleSession, err := m.Acquire("idle")
	if err != nil {
		t.Fatalf("Acquire(idle) error = %v", err)
	}
	pinnedSession, err := m.Acquire("pinned")
	if err != nil {
		t.Fatalf("Acquire(pinned) error = %v", err)
	}
	if err := m.AdmitViewer("idle", "viewer"); err != nil {
		t.Fatalf("AdmitViewer(idle) error = %v", err)
	}

	stale := time.Now().Add(-time.Hour)
	for _, s := range []*Session{idleSession, pinnedSession} {
		s.mu.Lock()
		s.lastTouch = stale
		s.mu.Unlock()
	}
	m.reapOnce()

	if _, ok := m.Get("idle"); ok {
		t.Fatal("idle on-demand session survived the reaper")
	}
	if got := idleSession.State(); got != "stopped" {
		t.Fatalf("reaped session state = %q, want stopped", got)
	}
	current, ok := m.Get("pinned")
	if !ok || current != pinnedSession {
		t.Fatal("always-on session was reaped")
	}
	if got := pinnedSession.State(); got != "running" {
		t.Fatalf("always-on session state = %q, want running", got)
	}

	m.mu.Lock()
	_, leases := m.viewers["idle"]
	m.mu.Unlock()
	if leases {
		t.Fatal("reaped channel kept viewer leases")
	}
}
