// Package hls publishes decrypted CMAF tracks as HLS fMP4. Assets become
// visible only once they are complete, and the playlist never points at
// anything that is not already readable.
package hls

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Kind string

const (
	KindVideo Kind = "video"
	KindAudio Kind = "audio"
)

type Track struct {
	Name      string
	Kind      Kind
	Codec     string
	Bandwidth int
	Width     int
	Height    int
	Channels  int
	Lang      string
}

type Config struct {
	Dir string
	// PlaylistSize is how many segments a live media playlist advertises.
	PlaylistSize int
	// Grace is how long a segment stays readable after it leaves the playlist,
	// so a player holding an older playlist does not hit a hard 404.
	Grace time.Duration
	// Static emits EXT-X-ENDLIST and never expires segments.
	Static bool
	Now    func() time.Time
}

type segment struct {
	Name          string
	Seq           uint64
	Duration      float64
	Discontinuity bool
	expiresAt     time.Time
}

type track struct {
	Track
	init                  []byte
	initReady             bool
	segments              []segment
	tombstones            []segment
	mediaSequence         uint64
	discontinuitySequence uint64
	// frontier is the highest sequence ever published. It only moves forward:
	// a late segment from before it is dropped, never re-inserted.
	frontier    uint64
	hasFrontier bool
}

func (t *track) initName() string     { return t.Name + "-init.mp4" }
func (t *track) playlistName() string { return t.Name + ".m3u8" }
func segmentName(track string, seq uint64) string {
	return fmt.Sprintf("%s-%06d.m4s", track, seq)
}

type Publisher struct {
	cfg Config
	now func() time.Time

	mu        sync.RWMutex
	order     []string
	tracks    map[string]*track
	playlists map[string][]byte
	assets    map[string]string
	playable  bool
}

func New(cfg Config) (*Publisher, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("hls: no output directory")
	}
	if cfg.PlaylistSize <= 0 {
		cfg.PlaylistSize = 8
	}
	if cfg.Grace <= 0 {
		cfg.Grace = 30 * time.Second
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("hls: create output dir: %w", err)
	}
	return &Publisher{
		cfg:       cfg,
		now:       cfg.Now,
		tracks:    map[string]*track{},
		playlists: map[string][]byte{},
		assets:    map[string]string{},
	}, nil
}

// AddTrack registers a track. All tracks must be added before the first
// segment is published; the engine is chosen at startup and cannot change the
// topology afterwards.
func (p *Publisher) AddTrack(t Track) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t.Name == "" {
		return fmt.Errorf("hls: track without a name")
	}
	if _, ok := p.tracks[t.Name]; ok {
		return fmt.Errorf("hls: track %s already exists", t.Name)
	}
	if p.playable {
		return fmt.Errorf("hls: cannot add track %s after publishing", t.Name)
	}
	p.tracks[t.Name] = &track{Track: t}
	p.order = append(p.order, t.Name)
	return nil
}

func (p *Publisher) PublishInit(name string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tracks[name]
	if !ok {
		return fmt.Errorf("hls: unknown track %s", name)
	}
	if err := p.writeAsset(t.initName(), data); err != nil {
		return err
	}
	t.init = data
	t.initReady = true
	return nil
}

// PublishSegment writes a segment, then makes it visible. Anything at or below
// the frontier is a late arrival and is discarded rather than reordering a
// window players may already have fetched.
func (p *Publisher) PublishSegment(name string, seq uint64, duration float64, data []byte, discontinuity bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tracks[name]
	if !ok {
		return fmt.Errorf("hls: unknown track %s", name)
	}
	if !t.initReady {
		return fmt.Errorf("hls: track %s has no init segment", name)
	}
	if t.hasFrontier && seq <= t.frontier {
		return nil
	}
	if duration <= 0 {
		return fmt.Errorf("hls: track %s segment %d has zero duration", name, seq)
	}

	file := segmentName(name, seq)
	if err := p.writeAsset(file, data); err != nil {
		return err
	}
	t.segments = append(t.segments, segment{
		Name:          file,
		Seq:           seq,
		Duration:      duration,
		Discontinuity: discontinuity,
	})
	t.frontier = seq
	t.hasFrontier = true
	p.slide(t)
	p.refresh()
	return nil
}

// slide trims the playlist window. Segments leaving it are kept readable for
// the grace period before deletion.
func (p *Publisher) slide(t *track) {
	if p.cfg.Static {
		return
	}
	for len(t.segments) > p.cfg.PlaylistSize {
		gone := t.segments[0]
		t.segments = t.segments[1:]
		t.mediaSequence++
		if gone.Discontinuity {
			t.discontinuitySequence++
		}
		gone.expiresAt = p.now().Add(p.cfg.Grace)
		t.tombstones = append(t.tombstones, gone)
	}
	p.reap(t)
}

func (p *Publisher) reap(t *track) {
	now := p.now()
	kept := t.tombstones[:0]
	for _, s := range t.tombstones {
		if now.Before(s.expiresAt) {
			kept = append(kept, s)
			continue
		}
		delete(p.assets, s.Name)
		_ = os.Remove(filepath.Join(p.cfg.Dir, s.Name))
	}
	t.tombstones = kept
}

// refresh rebuilds the playlist snapshots. Unchanged playlists are not
// rewritten, so a poller does not churn buffers for nothing.
func (p *Publisher) refresh() {
	ready := true
	tracks := make([]*track, 0, len(p.order))
	for _, name := range p.order {
		t := p.tracks[name]
		tracks = append(tracks, t)
		if !t.initReady || len(t.segments) == 0 {
			ready = false
		}
	}
	if !ready {
		return
	}
	p.playable = true

	for _, t := range tracks {
		next := mediaPlaylist(t, p.cfg.Static)
		if cur, ok := p.playlists[t.playlistName()]; ok && string(cur) == string(next) {
			continue
		}
		p.playlists[t.playlistName()] = next
	}
	master := masterPlaylist(tracks, true)
	if cur, ok := p.playlists[MasterName]; !ok || string(cur) != string(master) {
		p.playlists[MasterName] = master
	}
}

// writeAsset publishes bytes under a name only after the full content is on
// disk, so a reader can never observe a partial segment.
func (p *Publisher) writeAsset(name string, data []byte) error {
	final := filepath.Join(p.cfg.Dir, name)
	tmp, err := os.CreateTemp(p.cfg.Dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("hls: create temp asset: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("hls: write asset %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("hls: sync asset %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hls: close asset %s: %w", name, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		return fmt.Errorf("hls: publish asset %s: %w", name, err)
	}
	tmpName = ""
	p.assets[name] = final
	return nil
}

// Playable reports whether every track has an init segment and at least one
// media segment, which is the point a player can actually start.
func (p *Publisher) Playable() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.playable
}

func (p *Publisher) Playlist(name string) ([]byte, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pl, ok := p.playlists[name]
	return pl, ok
}

// Asset resolves a published asset name to its path. Names not registered here
// are not served: the HTTP layer must never map a request path onto the work
// directory itself.
func (p *Publisher) Asset(name string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	path, ok := p.assets[name]
	return path, ok
}

// Frontier is the highest published sequence per track.
func (p *Publisher) Frontier() map[string]uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]uint64, len(p.tracks))
	for name, t := range p.tracks {
		out[name] = t.frontier
	}
	return out
}
