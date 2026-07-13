package packager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type expiringOrigin struct {
	*liveOrigin

	smu     sync.Mutex
	session int

	live      int
	entryHits int
}

func newExpiringOrigin(t *testing.T) *expiringOrigin {
	o := &expiringOrigin{liveOrigin: newLiveOrigin(t), session: 1, live: 1}
	o.prefix = "s1/"
	return o
}

func (o *expiringOrigin) expire() {
	o.smu.Lock()
	defer o.smu.Unlock()
	o.session++
	o.live = o.session
}

func (o *expiringOrigin) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	if strings.Contains(url, "stream.mpd") {
		o.smu.Lock()
		session := o.session
		if i := strings.Index(url, "session="); i >= 0 {

			_, _ = fmt.Sscanf(url[i:], "session=%d", &session)
		} else {
			o.entryHits++
		}
		o.smu.Unlock()

		o.mu.Lock()
		o.prefix = fmt.Sprintf("s%d/", session)
		o.mu.Unlock()
		return o.manifest(), fmt.Sprintf("%s?session=%d", entryURL, session), nil
	}

	var session int
	if i := strings.Index(url, "/live/s"); i >= 0 {
		_, _ = fmt.Sscanf(url[i:], "/live/s%d/", &session)
	}
	o.smu.Lock()
	live := o.live
	o.smu.Unlock()
	if session != live {
		return nil, "", fmt.Errorf("403: session %d is expired", session)
	}
	return o.liveOrigin.Fetch(ctx, url)
}

func (o *expiringOrigin) resolvedFromEntry() int {
	o.smu.Lock()
	defer o.smu.Unlock()
	return o.entryHits
}

func TestMediaThatStopsWhileTheManifestLivesForcesAReResolve(t *testing.T) {
	origin := newExpiringOrigin(t)
	clock := newClock()
	n := startExpiring(t, origin, clock)

	published := n.Stats().SegmentsPublished
	if published == 0 {
		t.Fatal("nothing was published before the session expired")
	}
	if origin.resolvedFromEntry() != 1 {
		t.Fatalf("entry url resolved %d times at startup, want 1", origin.resolvedFromEntry())
	}

	origin.expire()

	origin.grow(4)
	clock.advance(5 * time.Second)
	pres, err := n.refreshManifest(context.Background())
	if err != nil {
		t.Fatalf("the pinned manifest must still be readable, that is the whole trap: %v", err)
	}
	if err := n.advance(context.Background(), pres); err == nil {
		t.Fatal("segments from an expired session must not fetch cleanly")
	}
	if origin.resolvedFromEntry() != 1 {
		t.Error("a single failed fetch is not enough to abandon the session; it may be transient")
	}

	clock.advance(2 * n.opts.ReanchorAfter)
	pres, err = n.refreshManifest(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	_ = n.advance(context.Background(), pres)

	pres, err = n.refreshManifest(context.Background())
	if err != nil {
		t.Fatalf("refresh after the stall: %v", err)
	}
	if origin.resolvedFromEntry() < 2 {
		t.Fatal("the publication never went back to the entry url; a stalled channel stays stalled")
	}
	if err := n.advance(context.Background(), pres); err != nil {
		t.Fatalf("advance after re-resolve: %v", err)
	}
	if n.Stats().SegmentsPublished <= published {
		t.Fatalf("published %d segments, was %d: the channel did not recover",
			n.Stats().SegmentsPublished, published)
	}
	if n.Stats().Reresolves == 0 {
		t.Error("the re-resolve is not counted, so a channel doing this all night looks healthy")
	}
}

func TestRenamedRepresentationIsReboundNotFatal(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	published := n.Stats().SegmentsPublished
	origin.rename("v9", "a9")
	origin.grow(2)
	clock.advance(4 * time.Second)

	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("a renamed representation killed the channel: %v", err)
	}
	if n.Stats().SegmentsPublished <= published {
		t.Fatal("nothing was published after the representations were renamed")
	}
	if n.video.rep.ID != "v9" {
		t.Errorf("video is still bound to %s, not the representation that replaced it", n.video.rep.ID)
	}

	if n.Stats().Discontinuities == 0 {
		t.Error("re-binding to a new representation must be signalled as a discontinuity")
	}
}

func startExpiring(t *testing.T, origin *expiringOrigin, clock *fakeClock) *Native {
	t.Helper()
	n, err := StartNative(context.Background(), Options{
		ManifestURL:   entryURL,
		Dir:           t.TempDir(),
		Keys:          keys(t),
		Fetcher:       origin,
		StartSegments: 3,
		PlaylistSize:  10,
		ReanchorAfter: 30 * time.Second,
		Now:           clock.now,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = n.Stop() })
	return n
}

func TestAStalledPublicationFailsOnlyWhenTheManifestIsHealthy(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	if err := n.checkStalled(); err != nil {
		t.Fatalf("a publication that just started is not stalled: %v", err)
	}

	clock.advance(4 * time.Minute)

	if err := n.checkStalled(); err != nil {
		t.Fatalf("an unreachable upstream must keep retrying, not burn the restart budget: %v", err)
	}

	n.lastManifestOK = clock.now()
	err := n.checkStalled()
	if err == nil {
		t.Fatal("a live manifest with a dead publication means we are broken, and must not look healthy")
	}
	var fatal *fatalError
	if !errors.As(err, &fatal) {
		t.Errorf("stall must end the publication so the session restarts, got %T", err)
	}
}

func TestTheStallWatchdogCanBeDisabled(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)
	n.opts.StallTimeout = -1

	clock.advance(time.Hour)
	n.lastManifestOK = clock.now()
	if err := n.checkStalled(); err != nil {
		t.Fatalf("the watchdog must stay off when it is disabled: %v", err)
	}
}
