// Package hls publishes decrypted CMAF tracks as HLS fMP4.
package hls

import (
	"fmt"
	"math"
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
	Name string
	Kind Kind

	Label     string
	Codec     string
	Bandwidth int
	Width     int
	Height    int
	Channels  int
	Lang      string
}

type Config struct {
	Dir string

	PlaylistSize int

	Grace time.Duration

	Static bool

	MaxSegmentDuration time.Duration
	Now                func() time.Time
}

type Publication struct {
	Track         string
	Seq           uint64
	Duration      float64
	At            time.Time
	Discontinuity bool
}

type segment struct {
	Name          string
	Seq           uint64
	Duration      float64
	At            time.Time
	Discontinuity bool

	InitName  string
	expiresAt time.Time
}

type track struct {
	Track
	initName              string
	initCount             int
	initReady             bool
	segments              []segment
	tombstones            []segment
	mediaSequence         uint64
	discontinuitySequence uint64

	frontier    uint64
	hasFrontier bool

	target int
}

func (t *track) setTarget(hint int, dur float64) {
	need := ceilSeconds(dur)
	if t.target == 0 {
		t.target = max(hint, need)
		return
	}
	t.target = max(t.target, need)
}

func ceilSeconds(d float64) int {
	if d <= 0 {
		return 1
	}
	return int(math.Ceil(d - 0.001))
}

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
	assetName := t.Name + "-init.mp4"
	if t.initCount > 0 {
		assetName = fmt.Sprintf("%s-init-%d.mp4", t.Name, t.initCount+1)
	}
	if err := p.writeAsset(assetName, data); err != nil {
		return err
	}
	t.initName = assetName
	t.initCount++
	t.initReady = true
	return nil
}

func (p *Publisher) PublishSegment(pub Publication, data []byte) error {
	staged, err := p.Stage(data)
	if err != nil {
		return err
	}
	return p.PublishStaged(pub, staged)
}

func (p *Publisher) PublishStaged(pub Publication, staged string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tracks[pub.Track]
	if !ok {
		p.Discard(staged)
		return fmt.Errorf("hls: unknown track %s", pub.Track)
	}
	if !t.initReady {
		p.Discard(staged)
		return fmt.Errorf("hls: track %s has no init segment", pub.Track)
	}
	if t.hasFrontier && pub.Seq <= t.frontier {
		p.Discard(staged)
		return nil
	}
	if pub.Duration <= 0 {
		p.Discard(staged)
		return fmt.Errorf("hls: track %s segment %d has zero duration", pub.Track, pub.Seq)
	}

	file := segmentName(pub.Track, pub.Seq)
	if err := p.moveIntoPlace(file, staged); err != nil {
		return err
	}
	var hint int
	if p.cfg.MaxSegmentDuration > 0 {
		hint = ceilSeconds(p.cfg.MaxSegmentDuration.Seconds())
	}
	t.setTarget(hint, pub.Duration)
	t.segments = append(t.segments, segment{
		Name:          file,
		Seq:           pub.Seq,
		Duration:      pub.Duration,
		At:            pub.At,
		Discontinuity: pub.Discontinuity,
		InitName:      t.initName,
	})
	t.frontier = pub.Seq
	t.hasFrontier = true
	p.slide(t)
	p.refresh()
	return nil
}

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

func (p *Publisher) Stage(data []byte) (string, error) {
	tmp, err := os.CreateTemp(p.cfg.Dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("hls: create temp asset: %w", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("hls: stage asset: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(name)
		return "", fmt.Errorf("hls: sync asset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("hls: close asset: %w", err)
	}
	return name, nil
}

func (p *Publisher) Discard(staged string) {
	if staged != "" {
		_ = os.Remove(staged)
	}
}

func (p *Publisher) writeAsset(name string, data []byte) error {
	staged, err := p.Stage(data)
	if err != nil {
		return err
	}
	return p.moveIntoPlace(name, staged)
}

func (p *Publisher) moveIntoPlace(name, staged string) error {
	final := filepath.Join(p.cfg.Dir, name)
	if err := os.Rename(staged, final); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("hls: publish asset %s: %w", name, err)
	}
	p.assets[name] = final
	return nil
}

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

func (p *Publisher) Asset(name string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	path, ok := p.assets[name]
	return path, ok
}

func (p *Publisher) Frontier() map[string]uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]uint64, len(p.tracks))
	for name, t := range p.tracks {
		out[name] = t.frontier
	}
	return out
}

func (p *Publisher) CacheUsage() (int64, int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var bytes int64
	items := 0
	for name := range p.assets {
		st, err := os.Stat(p.assets[name])
		if err != nil {
			continue
		}
		bytes += st.Size()
		items++
	}
	return bytes, items
}
