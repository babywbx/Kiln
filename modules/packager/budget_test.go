package packager

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestByteGateBoundsConcurrencyBySize(t *testing.T) {
	const (
		limit   = 64 << 20
		segment = 30 << 20
	)
	g := newByteGate(limit)

	var live, peak atomic.Int64
	var wg sync.WaitGroup
	release := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			held, err := g.acquire(context.Background(), segment)
			if err != nil {
				t.Error(err)
				return
			}
			n := live.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			<-release
			live.Add(-1)
			g.release(held)
		}()
	}

	waitFor(t, &live, 2)
	if got := peak.Load(); got > 2 {
		t.Fatalf("%d segments were in flight at once, the budget allows 2", got)
	}
	close(release)
	wg.Wait()

	if g.used != 0 {
		t.Errorf("gate leaked %d bytes", g.used)
	}
}

func TestByteGateAdmitsAnOversizedSegment(t *testing.T) {
	g := newByteGate(8 << 20)
	held, err := g.acquire(context.Background(), 64<<20)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if held != 8<<20 {
		t.Errorf("reserved %d bytes, want the whole budget", held)
	}
	g.release(held)
}

func TestByteGateUnblocksOnCancel(t *testing.T) {
	g := newByteGate(1 << 20)
	held, err := g.acquire(context.Background(), 1<<20)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer g.release(held)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, 1<<20)
		done <- err
	}()
	cancel()
	if err := <-done; err == nil {
		t.Fatal("a cancelled acquire must not keep waiting for the budget")
	}
}

func TestSegmentSizeIsEstimatedFromTheDeclaredBandwidth(t *testing.T) {
	if got := declaredSize(15_128_000, 8); got != 15_128_000 {
		t.Errorf("declaredSize = %d, want bandwidth/8 * 8s = 15128000", got)
	}
	if got := declaredSize(0, 8); got != initialSegmentEstimate {
		t.Errorf("declaredSize with no bandwidth = %d, want the fallback", got)
	}
}

func waitFor(t *testing.T, v *atomic.Int64, want int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if v.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("only %d acquired, want %d", v.Load(), want)
}
