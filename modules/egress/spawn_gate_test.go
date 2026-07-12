package egress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

// readyDelay is how long the fake packager takes to produce a playable
// playlist. It stands in for the 45-90 s readiness wait of a real source.
const (
	readyDelay      = 400 * time.Millisecond
	readyDelayShell = "0.4"
)

type recordingGate struct {
	sem chan struct{}

	mu    sync.Mutex
	holds []time.Duration
}

func newRecordingGate(n int) *recordingGate {
	return &recordingGate{sem: make(chan struct{}, n)}
}

func (g *recordingGate) Acquire(ctx context.Context) (func(), error) {
	select {
	case g.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	held := time.Now()
	return func() {
		g.mu.Lock()
		g.holds = append(g.holds, time.Since(held))
		g.mu.Unlock()
		<-g.sem
	}, nil
}

func (g *recordingGate) longestHold() time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	var max time.Duration
	for _, h := range g.holds {
		if h > max {
			max = h
		}
	}
	return max
}

// fakePackager stands in for ffmpeg: it takes readyDelay to write a playable
// playlist, then idles until it is killed.
func fakePackager(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-ffmpeg")
	script := `#!/bin/sh
for arg in "$@"; do index="$arg"; done
work=$(dirname "$index")
sleep ` + readyDelayShell + `
dd if=/dev/zero of="$work/seg_00000.ts" bs=1024 count=64 2>/dev/null
dd if=/dev/zero of="$work/seg_00001.ts" bs=1024 count=64 2>/dev/null
cat > "$index" <<EOF
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:2
#EXTINF:2.000,
seg_00000.ts
#EXTINF:2.000,
seg_00001.ts
EOF
sleep 60
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake packager: %v", err)
	}
	return path
}

// The gate must cover the launch only. If it also covered the readiness wait,
// N cold starts through a capacity-1 gate would serialize into N*readyDelay.
func TestSpawnGateDoesNotCoverReadinessWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake packager is a shell script")
	}
	origin := httptest.NewServer(http.FileServer(http.Dir(filepath.Join("..", "..", "testdata", "cenc", "hevc"))))
	defer origin.Close()

	gate := newRecordingGate(1)
	binary := fakePackager(t)

	const channels = 3
	jobs := make([]*DashJob, channels)
	errs := make([]error, channels)
	start := time.Now()

	var wg sync.WaitGroup
	for i := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			jobs[i], errs[i] = StartDashHLS(context.Background(), DashOptions{
				Binary:     binary,
				FFmpegMode: config.FFmpegModeNative,
				SourceURL:  origin.URL + "/stream.mpd",
				Keys:       []config.KeyPair{{KID: "ffeeddccbbaa99887766554433221100", Key: "00112233445566778899aabbccddeeff"}},
				WorkDir:    filepath.Join(t.TempDir(), "work"),
				SpawnGate:  gate,
			})
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("channel %d failed to start: %v", i, err)
		}
		defer jobs[i].Stop()
	}

	// The launch itself is milliseconds; the readiness wait is not.
	if hold := gate.longestHold(); hold >= readyDelay {
		t.Errorf("gate was held for %v, which covers the readiness wait (%v)", hold, readyDelay)
	}
	if elapsed >= channels*readyDelay {
		t.Errorf("%d cold starts took %v, i.e. they serialized behind the gate", channels, elapsed)
	}
}

// The gate still bounds launches: with capacity 1 no two spawns overlap.
func TestSpawnGateBoundsConcurrentLaunches(t *testing.T) {
	gate := newSpawnGate(t, 1)
	var live, peak int
	var mu sync.Mutex

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := gate.Acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			live++
			if live > peak {
				peak = live
			}
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			live--
			mu.Unlock()
			release()
		}()
	}
	wg.Wait()
	if peak != 1 {
		t.Errorf("peak concurrent launches = %d, want 1", peak)
	}
}

func newSpawnGate(t *testing.T, n int) SpawnGate {
	t.Helper()
	return newRecordingGate(n)
}
