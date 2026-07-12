package packager

import (
	"context"
	"sync"
)

// byteGate bounds how many segment bytes may be in flight at once, across every
// channel in the process.
//
// A count-based limit cannot do this job: three segments in flight is modest for
// a 2 MB rendition and eighty megabytes for a 4K one. Memory scales with bytes,
// so the limit has to be bytes.
type byteGate struct {
	mu    sync.Mutex
	cond  *sync.Cond
	limit int64
	used  int64
}

func newByteGate(limit int64) *byteGate {
	if limit <= 0 {
		limit = defaultInflightBytes
	}
	g := &byteGate{limit: limit}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// acquire reserves n bytes, waiting until they are free. A single reservation
// larger than the whole budget is clamped, so one oversized segment cannot
// deadlock the gate against itself.
func (g *byteGate) acquire(ctx context.Context, n int64) (int64, error) {
	if n <= 0 {
		n = 1
	}
	if n > g.limit {
		n = g.limit
	}

	stop := context.AfterFunc(ctx, func() {
		g.mu.Lock()
		g.cond.Broadcast()
		g.mu.Unlock()
	})
	defer stop()

	g.mu.Lock()
	defer g.mu.Unlock()
	for g.used+n > g.limit {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		g.cond.Wait()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	g.used += n
	return n, nil
}

func (g *byteGate) release(n int64) {
	if n <= 0 {
		return
	}
	g.mu.Lock()
	g.used -= n
	if g.used < 0 {
		g.used = 0
	}
	g.mu.Unlock()
	g.cond.Broadcast()
}
