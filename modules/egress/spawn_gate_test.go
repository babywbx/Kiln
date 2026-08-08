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
	"github.com/babywbx/kiln/modules/pull"
)

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
	took := make([]time.Duration, channels)
	start := time.Now()

	var wg sync.WaitGroup
	for i := range channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			began := time.Now()
			jobs[i], errs[i] = StartDashHLS(context.Background(), DashOptions{
				Binary:     binary,
				FFmpegMode: config.FFmpegModeNative,
				SourceURL:  origin.URL + "/stream.mpd",
				Keys:       []config.KeyPair{{KID: "ffeeddccbbaa99887766554433221100", Key: "00112233445566778899aabbccddeeff"}},
				WorkDir:    filepath.Join(t.TempDir(), "work"),
				SpawnGate:  gate,
				Pull:       pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
			})
			took[i] = time.Since(began)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("channel %d failed to start: %v", i, err)
		}
		defer func() { _ = jobs[i].Stop() }()
	}

	if hold := gate.longestHold(); hold >= readyDelay {
		t.Errorf("gate was held for %v, which covers the readiness wait (%v)", hold, readyDelay)
	}

	var total time.Duration
	for _, d := range took {
		total += d
	}
	if elapsed > total/2 {
		t.Errorf("%d cold starts took %v of a %v serial total: they did not overlap",
			channels, elapsed, total)
	}
}

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
