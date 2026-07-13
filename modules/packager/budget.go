package packager

import (
	"context"
	"sync"
)

type byteGate struct {
	mu      sync.Mutex
	limit   int64
	used    int64
	waiters []*byteWaiter
	changed chan struct{}
}

type byteWaiter struct {
	n     int64
	ready chan struct{}
}

type byteReservation struct {
	mu        sync.Mutex
	gate      *byteGate
	n         int64
	onRelease func()
	released  bool
}

func newByteGate(limit int64) *byteGate {
	if limit <= 0 {
		limit = defaultInflightBytes
	}
	return &byteGate{limit: limit, changed: make(chan struct{})}
}

func (g *byteGate) acquire(ctx context.Context, n int64) (*byteReservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n = normalizedBytes(n)
	w := &byteWaiter{n: n, ready: make(chan struct{})}

	g.mu.Lock()
	g.waiters = append(g.waiters, w)
	g.admitLocked()
	g.mu.Unlock()

	select {
	case <-w.ready:
		return &byteReservation{gate: g, n: n}, nil
	case <-ctx.Done():
		g.mu.Lock()
		for i, candidate := range g.waiters {
			if candidate == w {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				g.admitLocked()
				g.signalLocked()
				g.mu.Unlock()
				return nil, ctx.Err()
			}
		}
		g.mu.Unlock()
		<-w.ready
		g.release(n)
		return nil, ctx.Err()
	}
}

func (g *byteGate) admitLocked() {
	for len(g.waiters) > 0 {
		w := g.waiters[0]
		if w.n > g.limit {
			if g.used != 0 {
				return
			}
		} else if g.used > g.limit-w.n {
			return
		}
		g.waiters = g.waiters[1:]
		g.used += w.n
		close(w.ready)
		if w.n > g.limit {
			return
		}
	}
}

func (g *byteGate) release(n int64) {
	g.mu.Lock()
	g.used -= n
	g.admitLocked()
	g.signalLocked()
	g.mu.Unlock()
}

func (g *byteGate) signalLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}

func (g *byteGate) usage() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.used
}

func (g *byteGate) capacity() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit
}

func (r *byteReservation) resize(n int64) bool {
	n = normalizedBytes(n)
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return false
	}
	if n <= r.n {
		delta := r.n - n
		r.n = n
		r.mu.Unlock()
		r.gate.release(delta)
		return true
	}
	delta := n - r.n
	r.gate.mu.Lock()
	withinLimit := delta <= r.gate.limit && r.gate.used <= r.gate.limit-delta
	oversizedAlone := n > r.gate.limit && r.gate.used == r.n
	if len(r.gate.waiters) > 0 || !withinLimit && !oversizedAlone {
		r.gate.mu.Unlock()
		r.mu.Unlock()
		return false
	}
	r.gate.used += delta
	r.n = n
	r.gate.mu.Unlock()
	r.mu.Unlock()
	return true
}

func (r *byteReservation) resizeContext(ctx context.Context, n int64) bool {
	n = normalizedBytes(n)
	for {
		r.mu.Lock()
		if r.released {
			r.mu.Unlock()
			return false
		}
		if n <= r.n {
			delta := r.n - n
			r.n = n
			r.mu.Unlock()
			r.gate.release(delta)
			return true
		}

		delta := n - r.n
		r.gate.mu.Lock()
		withinLimit := delta <= r.gate.limit && r.gate.used <= r.gate.limit-delta
		oversizedAlone := n > r.gate.limit && r.gate.used == r.n
		if withinLimit || oversizedAlone {
			r.gate.used += delta
			r.n = n
			r.gate.mu.Unlock()
			r.mu.Unlock()
			return true
		}
		changed := r.gate.changed
		r.gate.mu.Unlock()
		r.mu.Unlock()

		select {
		case <-changed:
		case <-ctx.Done():
			return false
		}
	}
}

func (r *byteReservation) shrink(n int64) {
	n = normalizedBytes(n)
	r.mu.Lock()
	if r.released || n >= r.n {
		r.mu.Unlock()
		return
	}
	delta := r.n - n
	r.n = n
	r.mu.Unlock()
	r.gate.release(delta)
}

func (r *byteReservation) release() {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return
	}
	r.released = true
	n := r.n
	onRelease := r.onRelease
	r.n = 0
	r.mu.Unlock()
	r.gate.release(n)
	if onRelease != nil {
		onRelease()
	}
}

func (r *byteReservation) releaseCallback() {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return
	}
	onRelease := r.onRelease
	r.onRelease = nil
	r.mu.Unlock()
	if onRelease != nil {
		onRelease()
	}
}

func normalizedBytes(n int64) int64 {
	if n <= 0 {
		return 1
	}
	return n
}
