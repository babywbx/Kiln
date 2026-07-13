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
			reservation, err := g.acquire(context.Background(), segment)
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
			reservation.release()
		}()
	}

	waitFor(t, &live, 2)
	if got := peak.Load(); got > 2 {
		t.Fatalf("%d segments were in flight at once, the budget allows 2", got)
	}
	close(release)
	wg.Wait()

	if got := g.usage(); got != 0 {
		t.Errorf("gate leaked %d bytes", got)
	}
}

func TestByteGateAdmitsAnOversizedSegment(t *testing.T) {
	g := newByteGate(8 << 20)
	reservation, err := g.acquire(context.Background(), 64<<20)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if got := g.usage(); got != 64<<20 {
		t.Errorf("usage = %d, want the oversized reservation", got)
	}
	reservation.release()
}

func TestByteGateUnblocksOnCancel(t *testing.T) {
	g := newByteGate(1 << 20)
	reservation, err := g.acquire(context.Background(), 1<<20)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer reservation.release()

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

func gateWaiters(g *byteGate) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.waiters)
}

func TestByteGateFIFO(t *testing.T) {
	g := newByteGate(1)
	held, err := g.acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	order := make(chan int, 2)
	start := func(id int) {
		go func() {
			reservation, err := g.acquire(context.Background(), 1)
			if err != nil {
				t.Error(err)
				return
			}
			order <- id
			reservation.release()
		}()
	}
	start(1)
	for gateWaiters(g) != 1 {
		time.Sleep(time.Millisecond)
	}
	start(2)
	held.release()
	if got := <-order; got != 1 {
		t.Fatalf("first admitted = %d, want 1", got)
	}
	if got := <-order; got != 2 {
		t.Fatalf("second admitted = %d, want 2", got)
	}
}

func TestByteReservationShrinkKeepsLiveBudgetCovered(t *testing.T) {
	g := newByteGate(10)
	reservation, err := g.acquire(context.Background(), 8)
	if err != nil {
		t.Fatal(err)
	}
	reservation.shrink(6)
	if got := g.usage(); got != 6 {
		t.Fatalf("usage = %d, want 6", got)
	}
	reservation.shrink(9)
	if got := g.usage(); got != 6 {
		t.Fatalf("growth changed usage to %d", got)
	}
	reservation.release()
	if got := g.usage(); got != 0 {
		t.Fatalf("usage = %d, want 0", got)
	}
}

func TestByteReservationResizeRejectsGrowthWithoutCapacity(t *testing.T) {
	g := newByteGate(10)
	reservation, err := g.acquire(context.Background(), 6)
	if err != nil {
		t.Fatal(err)
	}
	other, err := g.acquire(context.Background(), 4)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.resize(7) {
		t.Fatal("growth succeeded without capacity")
	}
	if got := g.usage(); got != 10 {
		t.Fatalf("usage = %d, want 10", got)
	}
	other.release()
	if !reservation.resize(7) {
		t.Fatal("growth failed after capacity was released")
	}
	reservation.release()
}

func TestByteReservationResizeAdmitsOneOversizedWorkingSet(t *testing.T) {
	g := newByteGate(10)
	reservation, err := g.acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reservation.resize(15) {
		t.Fatal("exclusive reservation could not grow beyond the nominal budget")
	}
	if got := g.usage(); got != 15 {
		t.Fatalf("usage = %d, want 15", got)
	}
	reservation.release()
}

func TestByteReservationGrowthWaitsForAnotherStage(t *testing.T) {
	g := newByteGate(96)
	decrypt, err := g.acquire(context.Background(), 64)
	if err != nil {
		t.Fatal(err)
	}
	download, err := g.acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan bool, 1)
	go func() {
		done <- download.resizeContext(context.Background(), 48)
	}()
	select {
	case <-done:
		t.Fatal("download growth did not wait for decrypt memory")
	case <-time.After(20 * time.Millisecond):
	}

	decrypt.release()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("download growth failed after memory became available")
		}
	case <-time.After(time.Second):
		t.Fatal("download growth remained blocked")
	}
	download.release()
}

func TestByteReservationGrowthDoesNotDeadlockBehindANewWaiter(t *testing.T) {
	g := newByteGate(96)
	active, err := g.acquire(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan *byteReservation, 1)
	go func() {
		reservation, _ := g.acquire(context.Background(), 96)
		waiting <- reservation
	}()

	time.Sleep(20 * time.Millisecond)
	if !active.resizeContext(context.Background(), 32) {
		t.Fatal("active growth deadlocked behind a waiter")
	}
	active.release()
	select {
	case reservation := <-waiting:
		reservation.release()
	case <-time.After(time.Second):
		t.Fatal("waiter was not admitted after the active reservation released")
	}
}

func BenchmarkByteReservationResize(b *testing.B) {
	g := newByteGate(1 << 30)
	reservation, err := g.acquire(context.Background(), 32<<10)
	if err != nil {
		b.Fatal(err)
	}
	defer reservation.release()
	b.ReportAllocs()
	for b.Loop() {
		if !reservation.resize(64 << 10) {
			b.Fatal("resize failed")
		}
		reservation.shrink(32 << 10)
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
