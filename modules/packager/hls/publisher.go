package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/filecache"
	"github.com/babywbx/kiln/modules/timedmeta"
)

type Kind string

const (
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindSubtitle Kind = "subtitle"
)

type Track struct {
	Name string
	Kind Kind

	Label     string
	Codec     string
	Bandwidth int
	Width     int
	Height    int
	FrameRate float64
	Channels  int
	Lang      string
	Default   bool
	Forced    bool
}

type Config struct {
	Dir string

	PlaylistSize int

	Grace time.Duration

	Static bool
	LLHLS  bool

	PartTarget time.Duration

	MaxSegmentDuration time.Duration
	Now                func() time.Time
}

type Publication struct {
	Track         string
	Seq           uint64
	Duration      float64
	At            time.Time
	Discontinuity bool
	DateRanges    []timedmeta.DateRange
}

type PartPublication struct {
	Track       string
	MSN         uint64
	Part        uint64
	Duration    float64
	Independent bool
}

type PlaylistOptions struct {
	Skip bool
}

type PlaylistRequest struct {
	PlaylistOptions
	AfterRevision uint64
	MSN           *uint64
	Part          *uint64
}

type PlaylistView struct {
	Body     []byte
	Revision uint64
}

var (
	ErrPlaylistRequestAhead = errors.New("hls: playlist request is too far ahead")
	ErrPartWithoutMSN       = errors.New("hls: part request requires an MSN")
)

type partialSegment struct {
	Name        string
	MSN         uint64
	Index       uint64
	Duration    float64
	Independent bool
}

type segment struct {
	Name          string
	Seq           uint64
	MSN           uint64
	Duration      float64
	At            time.Time
	Discontinuity bool
	DateRanges    []timedmeta.DateRange

	InitName  string
	Parts     []partialSegment
	expiresAt time.Time
}

type track struct {
	Track
	initName              string
	initAssets            map[string]struct{}
	initCount             int
	initReady             bool
	segments              []segment
	tombstones            []segment
	mediaSequence         uint64
	discontinuitySequence uint64

	frontier    uint64
	hasFrontier bool
	complete    bool
	parts       []partialSegment
	hint        *partialSegment
	partTarget  float64

	encodedSegments int
	encodedTarget   int
	encodedEnd      bool
	staticDirty     bool

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

func subtitleSegmentName(track string, seq uint64) string {
	return fmt.Sprintf("%s-%06d.vtt", track, seq)
}

func partName(track string, msn, part uint64) string {
	return fmt.Sprintf("%s-part-%06d-%03d.m4s", track, msn, part)
}

func (t *track) nextMSN() uint64 {
	return t.mediaSequence + uint64(len(t.segments))
}

func (t *track) hasParts() bool {
	if len(t.parts) > 0 {
		return true
	}
	for _, s := range t.segments {
		if len(s.Parts) > 0 {
			return true
		}
	}
	return false
}

type playlistEncoder struct {
	media  func(*track, bool) []byte
	master func([]*track, bool) []byte
}

type assetRemoval struct {
	name           string
	retryInitTrack string
}

type assetRemovals struct {
	inline   [4]assetRemoval
	overflow []assetRemoval
	count    int
}

func (r *assetRemovals) add(removal assetRemoval) {
	if r.count < len(r.inline) {
		r.inline[r.count] = removal
	} else {
		r.overflow = append(r.overflow, removal)
	}
	r.count++
}

func (r *assetRemovals) at(index int) assetRemoval {
	if index < len(r.inline) {
		return r.inline[index]
	}
	return r.overflow[index-len(r.inline)]
}

type Publisher struct {
	cfg            Config
	now            func() time.Time
	removeFile     func(string) error
	dropAfterWrite func(*os.File) error

	mu         sync.RWMutex
	order      []string
	tracks     map[string]*track
	playlists  map[string][]byte
	revisions  map[string]uint64
	assets     map[string]string
	dateRanges map[string]timedmeta.DateRange
	playable   bool
	encoder    playlistEncoder
	changed    chan struct{}
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
	if cfg.LLHLS && cfg.PartTarget <= 0 {
		cfg.PartTarget = 500 * time.Millisecond
	}
	if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
		return nil, fmt.Errorf("hls: create output dir: %w", err)
	}
	return &Publisher{
		cfg:            cfg,
		now:            cfg.Now,
		removeFile:     os.Remove,
		dropAfterWrite: filecache.DropAfterWrite,
		tracks:         map[string]*track{},
		playlists:      map[string][]byte{},
		revisions:      map[string]uint64{},
		assets:         map[string]string{},
		dateRanges:     map[string]timedmeta.DateRange{},
		changed:        make(chan struct{}),
		encoder: playlistEncoder{
			media:  mediaPlaylist,
			master: masterPlaylist,
		},
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
	partTarget := 0.0
	if p.cfg.LLHLS {
		partTarget = p.cfg.PartTarget.Seconds()
	}
	p.tracks[t.Name] = &track{Track: t, initAssets: map[string]struct{}{}, partTarget: partTarget}
	p.order = append(p.order, t.Name)
	return nil
}

func (p *Publisher) PublishInit(name string, data []byte) error {
	p.mu.Lock()
	var removals assetRemovals
	defer func() {
		p.mu.Unlock()
		p.removeAssets(removals)
	}()
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
	t.initAssets[assetName] = struct{}{}
	t.initCount++
	t.initReady = true
	removals = p.reap(t)
	return nil
}

func (p *Publisher) PublishSegment(pub Publication, data []byte) error {
	staged, err := p.Stage(data)
	if err != nil {
		return err
	}
	return p.PublishStaged(pub, staged)
}

func (p *Publisher) PublishPart(pub PartPublication, data []byte) error {
	staged, err := p.Stage(data)
	if err != nil {
		return err
	}
	return p.PublishPartStaged(pub, staged)
}

func (p *Publisher) PublishPartStaged(pub PartPublication, staged string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tracks[pub.Track]
	if !ok {
		p.Discard(staged)
		return fmt.Errorf("hls: unknown track %s", pub.Track)
	}
	if !p.cfg.LLHLS {
		p.Discard(staged)
		return fmt.Errorf("hls: partial segments are disabled")
	}
	if !t.initReady {
		p.Discard(staged)
		return fmt.Errorf("hls: track %s has no init segment", pub.Track)
	}
	if pub.Duration <= 0 {
		p.Discard(staged)
		return fmt.Errorf("hls: track %s part %d has zero duration", pub.Track, pub.Part)
	}
	wantMSN := t.nextMSN()
	if pub.MSN < wantMSN {
		p.Discard(staged)
		return nil
	}
	if pub.MSN > wantMSN {
		p.Discard(staged)
		return fmt.Errorf("hls: part MSN %d is ahead of next MSN %d", pub.MSN, wantMSN)
	}
	wantPart := uint64(len(t.parts))
	if pub.Part < wantPart {
		p.Discard(staged)
		return nil
	}
	if pub.Part > wantPart {
		p.Discard(staged)
		return fmt.Errorf("hls: part %d is ahead of next part %d", pub.Part, wantPart)
	}

	file := partName(pub.Track, pub.MSN, pub.Part)
	if err := p.moveIntoPlace(file, staged); err != nil {
		return err
	}
	part := partialSegment{
		Name:        file,
		MSN:         pub.MSN,
		Index:       pub.Part,
		Duration:    pub.Duration,
		Independent: pub.Independent,
	}
	t.parts = append(t.parts, part)
	if pub.Duration > t.partTarget {
		t.partTarget = pub.Duration
	}
	p.setHint(t, pub.MSN, pub.Part+1)
	p.refresh(t)
	p.signalChange()
	return nil
}

func (p *Publisher) PublishStaged(pub Publication, staged string) error {
	p.mu.Lock()
	var removals assetRemovals
	defer func() {
		p.mu.Unlock()
		p.removeAssets(removals)
	}()
	t, ok := p.tracks[pub.Track]
	if !ok {
		p.Discard(staged)
		return fmt.Errorf("hls: unknown track %s", pub.Track)
	}
	if !t.initReady && t.Kind != KindSubtitle {
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
	if t.Kind == KindSubtitle {
		file = subtitleSegmentName(pub.Track, pub.Seq)
	}
	if err := p.moveIntoPlace(file, staged); err != nil {
		return err
	}
	var hint int
	if p.cfg.MaxSegmentDuration > 0 {
		hint = ceilSeconds(p.cfg.MaxSegmentDuration.Seconds())
	}
	t.setTarget(hint, pub.Duration)
	msn := t.nextMSN()
	parts := t.parts
	t.parts = nil
	t.segments = append(t.segments, segment{
		Name:          file,
		Seq:           pub.Seq,
		MSN:           msn,
		Duration:      pub.Duration,
		At:            pub.At,
		Discontinuity: pub.Discontinuity,
		InitName:      t.initName,
		Parts:         parts,
	})
	if t.Kind == KindVideo {
		p.ensureTrackDateRanges(t)
	}
	metadataChanged := p.applyDateRanges(pub.DateRanges)
	t.frontier = pub.Seq
	t.hasFrontier = true
	removals = p.slide(t)
	if t.Kind == KindVideo {
		p.ensureTrackDateRanges(t)
		p.pruneDateRanges()
	}
	if t.hasParts() {
		p.setHint(t, t.nextMSN(), 0)
	} else {
		t.hint = nil
	}
	p.refresh(t)
	if p.playable {
		for _, changed := range metadataChanged {
			if changed != t {
				p.refreshMedia(changed)
			}
		}
	}
	p.signalChange()
	return nil
}

func (p *Publisher) ensureTrackDateRanges(t *track) bool {
	changed := false
	for _, dateRange := range sortedDateRanges(p.dateRanges) {
		if p.upsertTrackDateRange(t, dateRange) {
			changed = true
		}
	}
	return changed
}

func (p *Publisher) applyDateRanges(observations []timedmeta.DateRange) []*track {
	if len(observations) == 0 {
		return nil
	}
	changed := make(map[string]*track)
	for _, observation := range observations {
		if observation.ID == "" || observation.StartDate.IsZero() {
			continue
		}
		merged := timedmeta.MergeDateRange(p.dateRanges[observation.ID], observation)
		p.dateRanges[merged.ID] = merged
		for _, name := range p.order {
			t := p.tracks[name]
			if t.Kind != KindVideo {
				continue
			}
			if p.upsertTrackDateRange(t, merged) {
				changed[name] = t
			}
		}
	}
	result := make([]*track, 0, len(changed))
	for _, name := range p.order {
		if t, ok := changed[name]; ok {
			result = append(result, t)
		}
	}
	return result
}

func (p *Publisher) upsertTrackDateRange(t *track, dateRange timedmeta.DateRange) bool {
	for segmentIndex := range t.segments {
		for dateRangeIndex := range t.segments[segmentIndex].DateRanges {
			if t.segments[segmentIndex].DateRanges[dateRangeIndex].ID == dateRange.ID {
				if t.segments[segmentIndex].DateRanges[dateRangeIndex].Equal(dateRange) {
					return false
				}
				t.segments[segmentIndex].DateRanges[dateRangeIndex] = dateRange
				if segmentIndex < t.encodedSegments {
					t.staticDirty = true
				}
				return true
			}
		}
	}
	segmentIndex := dateRangeSegmentIndex(t.segments, dateRange)
	if segmentIndex < 0 {
		return false
	}
	t.segments[segmentIndex].DateRanges = append(t.segments[segmentIndex].DateRanges, dateRange)
	if segmentIndex < t.encodedSegments {
		t.staticDirty = true
	}
	return true
}

func dateRangeSegmentIndex(segments []segment, dateRange timedmeta.DateRange) int {
	if dateRange.StartDate.IsZero() {
		return -1
	}
	rangeEnd := dateRangeEnd(dateRange)
	for index, segment := range segments {
		if segment.At.IsZero() || segment.Duration <= 0 {
			continue
		}
		segmentEnd := segment.At.Add(time.Duration(segment.Duration * float64(time.Second)))
		if !dateRange.StartDate.Before(segment.At) && dateRange.StartDate.Before(segmentEnd) {
			return index
		}
		if dateRange.StartDate.Before(segment.At) && (rangeEnd == nil || rangeEnd.After(segment.At)) {
			return index
		}
	}
	return -1
}

func dateRangeEnd(dateRange timedmeta.DateRange) *time.Time {
	if dateRange.EndDate != nil {
		return dateRange.EndDate
	}
	if dateRange.Duration != nil && (dateRange.SCTE35In != "" || dateRange.SCTE35Cmd != "") {
		end := dateRange.StartDate.Add(*dateRange.Duration)
		return &end
	}
	return nil
}

func sortedDateRanges(ranges map[string]timedmeta.DateRange) []timedmeta.DateRange {
	result := make([]timedmeta.DateRange, 0, len(ranges))
	for _, dateRange := range ranges {
		result = append(result, dateRange)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].StartDate.Equal(result[j].StartDate) {
			return result[i].ID < result[j].ID
		}
		return result[i].StartDate.Before(result[j].StartDate)
	})
	return result
}

func (p *Publisher) pruneDateRanges() {
	for id, dateRange := range p.dateRanges {
		end := dateRangeEnd(dateRange)
		if end == nil {
			continue
		}
		visible := false
		for _, name := range p.order {
			t := p.tracks[name]
			if t.Kind != KindVideo {
				continue
			}
			for _, segment := range t.segments {
				for _, attached := range segment.DateRanges {
					if attached.ID == id {
						visible = true
						break
					}
				}
				if visible {
					break
				}
			}
			if visible || len(t.segments) == 0 {
				visible = true
				break
			}
			last := t.segments[len(t.segments)-1]
			if last.At.IsZero() || !last.At.Add(time.Duration(last.Duration*float64(time.Second))).After(*end) {
				visible = true
				break
			}
		}
		if !visible {
			delete(p.dateRanges, id)
		}
	}
}

func (p *Publisher) setHint(t *track, msn, part uint64) {
	t.hint = &partialSegment{
		Name:  partName(t.Name, msn, part),
		MSN:   msn,
		Index: part,
	}
}

func (p *Publisher) slide(t *track) assetRemovals {
	if p.cfg.Static {
		return assetRemovals{}
	}
	for len(t.segments) > p.cfg.PlaylistSize {
		gone := t.segments[0]
		t.segments[0] = segment{}
		t.segments = t.segments[1:]
		t.mediaSequence++
		if gone.Discontinuity {
			t.discontinuitySequence++
		}
		gone.expiresAt = p.now().Add(p.cfg.Grace)
		t.tombstones = append(t.tombstones, gone)
	}
	return p.reap(t)
}

func (p *Publisher) reap(t *track) assetRemovals {
	now := p.now()
	var removals assetRemovals
	kept := t.tombstones[:0]
	for _, s := range t.tombstones {
		if now.Before(s.expiresAt) {
			kept = append(kept, s)
			continue
		}
		delete(p.assets, s.Name)
		removals.add(assetRemoval{name: s.Name})
		for _, part := range s.Parts {
			delete(p.assets, part.Name)
			removals.add(assetRemoval{name: part.Name})
		}
	}
	clear(t.tombstones[len(kept):])
	t.tombstones = kept

	reachable := map[string]struct{}{t.initName: {}}
	for _, s := range t.segments {
		reachable[s.InitName] = struct{}{}
	}
	for _, s := range t.tombstones {
		reachable[s.InitName] = struct{}{}
	}
	for name := range t.initAssets {
		if _, ok := reachable[name]; ok {
			continue
		}
		delete(p.assets, name)
		delete(t.initAssets, name)
		removals.add(assetRemoval{name: name, retryInitTrack: t.Name})
	}
	return removals
}

func (p *Publisher) removeAssets(removals assetRemovals) {
	var retries assetRemovals
	for index := range removals.count {
		removal := removals.at(index)
		err := p.removeFile(filepath.Join(p.cfg.Dir, removal.name))
		if err != nil && !os.IsNotExist(err) && removal.retryInitTrack != "" {
			retries.add(removal)
		}
	}
	if retries.count == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for index := range retries.count {
		retry := retries.at(index)
		if t := p.tracks[retry.retryInitTrack]; t != nil {
			t.initAssets[retry.name] = struct{}{}
		}
	}
}

func (p *Publisher) refresh(changed *track) {
	if p.playable {
		p.refreshMedia(changed)
		return
	}

	tracks := make([]*track, 0, len(p.order))
	for _, name := range p.order {
		t := p.tracks[name]
		tracks = append(tracks, t)
		if t.Kind != KindSubtitle && (!t.initReady || len(t.segments) == 0) {
			return
		}
	}
	p.playable = true
	for _, t := range tracks {
		p.refreshMedia(t)
	}
	p.refreshMaster(tracks)
}

func (p *Publisher) refreshMedia(t *track) {
	name := t.playlistName()
	var next []byte
	if p.cfg.Static && !t.hasParts() {
		next = p.appendStaticPlaylist(t, p.playlists[name])
	} else {
		next = p.encoder.media(t, t.complete)
	}
	if cur, ok := p.playlists[name]; ok && bytes.Equal(cur, next) {
		return
	}
	p.playlists[name] = next
	p.revisions[name]++
}

func (p *Publisher) appendStaticPlaylist(t *track, current []byte) []byte {
	if t.encodedSegments == 0 || t.encodedTarget != t.target || t.encodedSegments > len(t.segments) || t.staticDirty {
		next := p.encoder.media(t, t.complete)
		t.encodedSegments = len(t.segments)
		t.encodedTarget = t.target
		t.encodedEnd = t.complete
		t.staticDirty = false
		return next
	}

	next := current
	currentMap := t.segments[t.encodedSegments-1].InitName
	for i := t.encodedSegments; i < len(t.segments); i++ {
		next = appendMediaSegment(next, t.segments[i], false, &currentMap)
	}
	if t.complete && !t.encodedEnd {
		next = append(next, "#EXT-X-ENDLIST\n"...)
	}
	t.encodedSegments = len(t.segments)
	t.encodedTarget = t.target
	t.encodedEnd = t.complete
	return next
}

func (p *Publisher) Complete() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, name := range p.order {
		t := p.tracks[name]
		t.complete = true
		t.hint = nil
		if p.playable {
			p.refreshMedia(t)
		}
	}
	p.signalChange()
}

func (p *Publisher) refreshMaster(tracks []*track) {
	next := p.encoder.master(tracks, true)
	if cur, ok := p.playlists[MasterName]; ok && bytes.Equal(cur, next) {
		return
	}
	p.playlists[MasterName] = next
	p.revisions[MasterName]++
}

func (p *Publisher) signalChange() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *Publisher) Stage(data []byte) (string, error) {
	return p.StageWrite(func(dst io.Writer) error {
		n, err := dst.Write(data)
		if err == nil && n != len(data) {
			return io.ErrShortWrite
		}
		return err
	})
}

func (p *Publisher) StageWrite(write func(io.Writer) error) (string, error) {
	tmp, err := os.CreateTemp(p.cfg.Dir, ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("hls: create temp asset: %w", err)
	}
	name := tmp.Name()
	completed := false
	defer func() {
		if !completed {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := write(tmp); err != nil {
		return "", fmt.Errorf("hls: stage asset: %w", err)
	}
	if err := p.dropAfterWrite(tmp); err != nil {
		return "", fmt.Errorf("hls: flush staged asset: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("hls: close asset: %w", err)
	}
	completed = true
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
	view, ok := p.PlaylistWithOptions(name, PlaylistOptions{})
	return view.Body, ok
}

func (p *Publisher) PlaylistWithOptions(name string, options PlaylistOptions) (PlaylistView, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.playlistViewLocked(name, options)
}

func (p *Publisher) playlistViewLocked(name string, options PlaylistOptions) (PlaylistView, bool) {
	pl, ok := p.playlists[name]
	if !ok {
		return PlaylistView{}, false
	}
	if options.Skip {
		if t := p.trackForPlaylistLocked(name); t != nil && !t.complete && t.hasParts() {
			pl = mediaPlaylistWithOptions(t, false, options)
		}
	}
	return PlaylistView{Body: pl, Revision: p.revisions[name]}, true
}

func (p *Publisher) PlaylistContext(ctx context.Context, name string, request PlaylistRequest) (PlaylistView, bool, error) {
	if request.Part != nil && request.MSN == nil {
		return PlaylistView{}, false, ErrPartWithoutMSN
	}
	for {
		p.mu.RLock()
		view, ok := p.playlistViewLocked(name, request.PlaylistOptions)
		if !ok {
			p.mu.RUnlock()
			return PlaylistView{}, false, nil
		}
		ready, err := p.playlistRequestReadyLocked(name, request, view.Revision)
		if ready && request.MSN != nil {
			if t := p.trackForPlaylistLocked(name); t != nil && *request.MSN < t.mediaSequence {
				view, _ = p.playlistViewLocked(name, PlaylistOptions{})
			}
		}
		changed := p.changed
		p.mu.RUnlock()
		if err != nil {
			return PlaylistView{}, true, err
		}
		if ready {
			return view, true, nil
		}
		select {
		case <-ctx.Done():
			return PlaylistView{}, true, ctx.Err()
		case <-changed:
		}
	}
}

func (p *Publisher) playlistRequestReadyLocked(name string, request PlaylistRequest, revision uint64) (bool, error) {
	t := p.trackForPlaylistLocked(name)
	if request.MSN == nil {
		return revision > request.AfterRevision, nil
	}
	if t == nil {
		return false, fmt.Errorf("hls: blocking reload is only available for media playlists")
	}
	if t.complete || *request.MSN < t.mediaSequence {
		return true, nil
	}

	lastKnown := t.nextMSN()
	if len(t.parts) == 0 && lastKnown > 0 {
		lastKnown--
	}
	if *request.MSN > lastKnown+2 {
		return false, ErrPlaylistRequestAhead
	}
	if request.Part == nil {
		return *request.MSN < t.nextMSN(), nil
	}
	if *request.MSN < t.nextMSN() {
		return true, nil
	}
	if *request.MSN == t.nextMSN() && *request.Part < uint64(len(t.parts)) {
		return true, nil
	}

	partTarget := t.partTarget
	if partTarget <= 0 {
		partTarget = p.cfg.PartTarget.Seconds()
	}
	advanceLimit := uint64(3)
	if partTarget > 0 {
		advanceLimit = uint64(math.Ceil(3 / partTarget))
		if advanceLimit < 3 {
			advanceLimit = 3
		}
	}
	lastPart := uint64(0)
	if len(t.parts) > 0 {
		lastPart = t.parts[len(t.parts)-1].Index
	}
	if *request.MSN >= t.nextMSN() && *request.Part > lastPart+advanceLimit {
		return false, ErrPlaylistRequestAhead
	}
	return false, nil
}

func (p *Publisher) trackForPlaylistLocked(name string) *track {
	for _, trackName := range p.order {
		t := p.tracks[trackName]
		if t.playlistName() == name {
			return t
		}
	}
	return nil
}

func (p *Publisher) Asset(name string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	path, ok := p.assets[name]
	return path, ok
}

func (p *Publisher) AssetContext(ctx context.Context, name string) (string, bool, error) {
	for {
		p.mu.RLock()
		if path, ok := p.assets[name]; ok {
			p.mu.RUnlock()
			return path, true, nil
		}
		active := p.isActiveHintLocked(name)
		changed := p.changed
		p.mu.RUnlock()
		if !active {
			return "", false, nil
		}
		select {
		case <-ctx.Done():
			return "", true, ctx.Err()
		case <-changed:
		}
	}
}

func (p *Publisher) isActiveHintLocked(name string) bool {
	for _, trackName := range p.order {
		t := p.tracks[trackName]
		if !t.complete && t.hint != nil && t.hint.Name == name {
			return true
		}
	}
	return false
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
