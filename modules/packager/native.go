package packager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

// Fetcher is how the packager reaches upstream. Proxy routing, SSRF checks and
// redirect policy live behind it and never leak into the packaging code.
// Implementations must be safe for concurrent use.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (data []byte, finalURL string, err error)
}

const (
	trackVideo = "video-main"
	trackAudio = "audio-main"
)

type Options struct {
	ManifestURL  string
	Dir          string
	Keys         cmaf.KeySet
	Fetcher      Fetcher
	PreferHeight int
	PlaylistSize int
	// StartSegments is how many already-published segments the first playlist
	// carries. It never waits for a segment the origin has not produced yet.
	StartSegments int
	// Prefetch bounds concurrent upstream segment fetches per track.
	Prefetch        int
	MaxSegmentBytes int64
	Grace           time.Duration
	// ReanchorAfter is how long a dynamic publication may make no progress
	// before it stops trusting its position and relocates the live edge.
	ReanchorAfter time.Duration
	Now           func() time.Time
	Log           *slog.Logger
}

const (
	defaultStartSegments   = 3
	defaultPrefetch        = 3
	defaultMaxSegmentBytes = 32 << 20
	defaultReanchorAfter   = 30 * time.Second
	defaultRefreshInterval = 2 * time.Second
)

func (o *Options) applyDefaults() {
	if o.StartSegments <= 0 {
		o.StartSegments = defaultStartSegments
	}
	if o.Prefetch <= 0 {
		o.Prefetch = defaultPrefetch
	}
	if o.MaxSegmentBytes <= 0 {
		o.MaxSegmentBytes = defaultMaxSegmentBytes
	}
	if o.ReanchorAfter <= 0 {
		o.ReanchorAfter = defaultReanchorAfter
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// trackState is one published track's position in the upstream timeline.
//
// The published sequence is ours, not the manifest's. Tying it to the upstream
// segment number would deadlock the channel on an origin rollover: the numbers
// restart at 1, land below the frontier, and every new segment gets discarded
// as a late arrival forever.
type trackState struct {
	name string
	rep  mpd.Representation
	init *cmaf.Init

	nextSeq uint64
	// lastTime is the upstream presentation time of the last published segment,
	// used to recognize what is genuinely new.
	lastTime uint64
	hasLast  bool
	// expectedDTS is where the next segment should start on the media timeline.
	// A segment that does not land there is a discontinuity.
	expectedDTS uint64
	hasExpected bool
	// forceDiscontinuity marks the next publish as a break, after a re-anchor
	// or an init change.
	forceDiscontinuity bool
	lastProgress       time.Time
}

// Native is a running native_rewrite publication.
type Native struct {
	opts Options
	now  func() time.Time
	log  *slog.Logger

	pub      *hls.Publisher
	plan     Plan
	packMode string

	video *trackState
	audio *trackState

	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	err         error
	intentional bool
	reanchors   int
}

// FallbackError says the native path declined this input at startup. The
// caller decides whether ffmpeg is a safe substitute; Allowed reports whether
// it would even be correct.
type FallbackError struct {
	Reason  string
	Allowed bool
	Err     error
}

func (e *FallbackError) Error() string {
	return fmt.Sprintf("native unsupported (%s): %v", e.Reason, e.Err)
}
func (e *FallbackError) Unwrap() error { return e.Err }

// StartNative prepares a publication and returns once it is playable: both
// init segments and the opening media segments of each primary track are on
// disk. It does not wait for a second segment to prove health; that continues
// in the background.
func StartNative(ctx context.Context, opts Options) (*Native, error) {
	if opts.Fetcher == nil {
		return nil, errors.New("packager: no fetcher")
	}
	opts.applyDefaults()

	pres, err := fetchManifest(ctx, opts)
	if err != nil {
		return nil, err
	}
	plan, err := PlanFromManifest(pres, opts.PreferHeight)
	if err != nil {
		return nil, err
	}
	if !plan.Native() {
		return nil, &FallbackError{Reason: plan.Reason, Allowed: plan.FallbackAllowed,
			Err: errors.New("manifest is outside the native support matrix")}
	}

	videoInit, audioInit, err := fetchInits(ctx, opts, plan)
	if err != nil {
		return nil, err
	}
	if err := VerifyTracks(&plan, videoInit.Track, audioInit.Track, opts.Keys); err != nil {
		return nil, &FallbackError{Reason: plan.Reason, Allowed: plan.FallbackAllowed, Err: err}
	}

	pub, err := hls.New(hls.Config{
		Dir:          opts.Dir,
		PlaylistSize: opts.PlaylistSize,
		Grace:        opts.Grace,
		Static:       !pres.Dynamic,
		Now:          opts.Now,
	})
	if err != nil {
		return nil, err
	}

	now := opts.Now()
	n := &Native{
		opts:     opts,
		now:      opts.Now,
		log:      opts.Log,
		pub:      pub,
		plan:     plan,
		packMode: packMode(pres, plan),
		done:     make(chan struct{}),
		video:    &trackState{name: trackVideo, rep: plan.Video, init: videoInit, nextSeq: 1, lastProgress: now},
		audio:    &trackState{name: trackAudio, rep: plan.Audio, init: audioInit, nextSeq: 1, lastProgress: now},
	}
	if err := n.registerTracks(); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel

	if err := n.publishFirst(ctx, pres); err != nil {
		cancel()
		return nil, err
	}

	go n.run(runCtx, pres)
	return n, nil
}

func fetchManifest(ctx context.Context, opts Options) (*mpd.Presentation, error) {
	raw, finalURL, err := opts.Fetcher.Fetch(ctx, opts.ManifestURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	pres, err := mpd.Parse(raw, finalURL)
	if err != nil {
		return nil, &FallbackError{Reason: ReasonAddressing, Allowed: true, Err: err}
	}
	return pres, nil
}

func fetchInits(ctx context.Context, opts Options, plan Plan) (*cmaf.Init, *cmaf.Init, error) {
	type result struct {
		init *cmaf.Init
		err  error
	}
	get := func(rep mpd.Representation) result {
		init, err := loadInit(ctx, opts, rep)
		return result{init: init, err: err}
	}

	var video, audio result
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); video = get(plan.Video) }()
	go func() { defer wg.Done(); audio = get(plan.Audio) }()
	wg.Wait()

	if video.err != nil {
		return nil, nil, video.err
	}
	if audio.err != nil {
		return nil, nil, audio.err
	}
	if video.init.Track.Kind != cmaf.KindVideo || audio.init.Track.Kind != cmaf.KindAudio {
		return nil, nil, &FallbackError{Reason: cmaf.ReasonSampleEntry, Allowed: true,
			Err: errors.New("init segments do not match the manifest content types")}
	}
	return video.init, audio.init, nil
}

func loadInit(ctx context.Context, opts Options, rep mpd.Representation) (*cmaf.Init, error) {
	if rep.Addressing.InitURL == "" {
		return nil, &FallbackError{Reason: ReasonAddressing, Allowed: true,
			Err: fmt.Errorf("representation %s has no initialization segment", rep.ID)}
	}
	raw, _, err := opts.Fetcher.Fetch(ctx, rep.Addressing.InitURL)
	if err != nil {
		return nil, fmt.Errorf("fetch init %s: %w", rep.ID, err)
	}
	init, err := cmaf.ParseInit(raw)
	if err != nil {
		if u, ok := cmaf.Unsupported(err); ok {
			return nil, &FallbackError{Reason: u.Reason, Allowed: true, Err: err}
		}
		return nil, fmt.Errorf("parse init %s: %w", rep.ID, err)
	}
	return init, nil
}

func (n *Native) registerTracks() error {
	if err := n.pub.AddTrack(hls.Track{
		Name:      trackVideo,
		Kind:      hls.KindVideo,
		Codec:     n.video.init.Track.Codec,
		Bandwidth: n.plan.Video.Bandwidth,
		Width:     n.plan.Video.Width,
		Height:    n.plan.Video.Height,
	}); err != nil {
		return err
	}
	if err := n.pub.AddTrack(hls.Track{
		Name:      trackAudio,
		Kind:      hls.KindAudio,
		Codec:     n.audio.init.Track.Codec,
		Bandwidth: n.plan.Audio.Bandwidth,
		Channels:  n.plan.Audio.AudioChannels,
		Lang:      n.plan.Audio.Lang,
	}); err != nil {
		return err
	}
	if err := n.pub.PublishInit(trackVideo, n.video.init.Clear); err != nil {
		return err
	}
	return n.pub.PublishInit(trackAudio, n.audio.init.Clear)
}

// publishFirst gets to playable without waiting for anything the origin has
// not published yet: it takes the opening window of segments that already
// exist and fetches them in parallel across both tracks.
func (n *Native) publishFirst(ctx context.Context, pres *mpd.Presentation) error {
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, ts := range []*trackState{n.video, n.audio} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			segs, err := pres.AvailableSegments(0, ts.rep, n.now())
			if err != nil {
				errs[i] = err
				return
			}
			if len(segs) == 0 {
				errs[i] = &FallbackError{Reason: ReasonAddressing, Allowed: true,
					Err: fmt.Errorf("no segment is available yet for %s", ts.rep.ID)}
				return
			}
			errs[i] = n.publishSegments(ctx, ts, n.startWindow(pres, segs))
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// startWindow picks where playback begins. A live stream starts near the edge
// with enough segments behind it for a player to buffer; a static one starts
// at the beginning.
func (n *Native) startWindow(pres *mpd.Presentation, segs []mpd.Segment) []mpd.Segment {
	if !pres.Dynamic {
		if len(segs) > n.opts.StartSegments {
			return segs[:n.opts.StartSegments]
		}
		return segs
	}
	if len(segs) > n.opts.StartSegments {
		return segs[len(segs)-n.opts.StartSegments:]
	}
	return segs
}

type prepared struct {
	data     []byte
	dur      float64
	baseTime uint64
	duration uint64
	err      error
}

// publishSegments fetches and decrypts in parallel but publishes strictly in
// order: the frontier only moves forward, so an out-of-order publish would
// silently drop everything behind it.
func (n *Native) publishSegments(ctx context.Context, ts *trackState, segs []mpd.Segment) error {
	if len(segs) == 0 {
		return nil
	}
	results := make([]prepared, len(segs))
	sem := make(chan struct{}, n.opts.Prefetch)

	var wg sync.WaitGroup
	for i, seg := range segs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i].err = ctx.Err()
				return
			}
			results[i] = n.prepare(ctx, ts, seg)
		}()
	}
	wg.Wait()

	for i, seg := range segs {
		if results[i].err != nil {
			return results[i].err
		}
		if err := n.commit(ts, seg, results[i]); err != nil {
			return err
		}
	}
	return nil
}

// commit publishes one prepared segment and advances the track's position.
// Continuity is judged on the media timeline the samples actually carry, not on
// the manifest's arithmetic.
func (n *Native) commit(ts *trackState, seg mpd.Segment, p prepared) error {
	discontinuity := ts.forceDiscontinuity
	if ts.hasExpected && !continuous(ts.expectedDTS, p.baseTime, ts.init.Track.Timescale) {
		discontinuity = true
		n.log.Info("timeline discontinuity",
			"track", ts.name, "expected_dts", ts.expectedDTS, "got_dts", p.baseTime)
	}

	if err := n.pub.PublishSegment(ts.name, ts.nextSeq, p.dur, p.data, discontinuity); err != nil {
		return err
	}

	ts.nextSeq++
	ts.lastTime = seg.Time
	ts.hasLast = true
	ts.expectedDTS = p.baseTime + p.duration
	ts.hasExpected = true
	ts.forceDiscontinuity = false
	ts.lastProgress = n.now()
	return nil
}

// continuous allows a small rounding drift, since some packagers round segment
// boundaries, but nothing that would actually skip or repeat media.
func continuous(expected, got uint64, timescale uint32) bool {
	if timescale == 0 {
		return expected == got
	}
	tolerance := uint64(timescale) / 100 // 10 ms
	if got >= expected {
		return got-expected <= tolerance
	}
	return expected-got <= tolerance
}

func (n *Native) prepare(ctx context.Context, ts *trackState, seg mpd.Segment) (res prepared) {
	raw, _, err := n.opts.Fetcher.Fetch(ctx, seg.URL)
	if err != nil {
		res.err = fmt.Errorf("fetch segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}
	if n.opts.MaxSegmentBytes > 0 && int64(len(raw)) > n.opts.MaxSegmentBytes {
		res.err = fmt.Errorf("segment %s#%d is %d bytes, over the limit", ts.rep.ID, seg.Number, len(raw))
		return res
	}
	clear, err := ts.init.Decrypt(raw, n.opts.Keys)
	if err != nil {
		res.err = fmt.Errorf("decrypt segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}
	res.data = clear.Clear
	res.baseTime = clear.BaseTime
	res.duration = clear.Duration
	res.dur = seg.Seconds(ts.rep.Addressing.Timescale)
	if clear.Duration > 0 && ts.init.Track.Timescale > 0 {
		res.dur = float64(clear.Duration) / float64(ts.init.Track.Timescale)
	}
	return res
}

// run keeps the publication moving after it is playable. Static inputs simply
// drain; dynamic inputs re-read the manifest on its own update period, with no
// fixed-interval polling of our own.
func (n *Native) run(ctx context.Context, pres *mpd.Presentation) {
	defer close(n.done)

	if !pres.Dynamic {
		if err := n.advance(ctx, pres); err != nil {
			n.fail(err)
		}
		return
	}

	interval := pres.MinimumUpdatePeriod
	if interval <= 0 {
		interval = defaultRefreshInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		next, err := fetchManifest(ctx, n.opts)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			n.log.Warn("mpd refresh failed", "err", err)
			timer.Reset(interval)
			continue
		}
		if err := n.advance(ctx, next); err != nil {
			if ctx.Err() != nil {
				return
			}
			var fatal *fatalError
			if errors.As(err, &fatal) {
				n.fail(err)
				return
			}
			n.log.Warn("segment publish failed", "err", err)
		}
		if p := next.MinimumUpdatePeriod; p > 0 {
			interval = p
		}
		timer.Reset(interval)
	}
}

// fatalError ends the publication instead of being retried on the next refresh.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// refreshPlan re-resolves the chosen representations against the new manifest.
// Each track's addressing, including its timeline, lives on the representation,
// so skipping this would keep scheduling against a stale window.
func (n *Native) refreshPlan(pres *mpd.Presentation) error {
	for _, ts := range []*trackState{n.video, n.audio} {
		rep, ok := findRepresentation(pres, ts.rep.ID)
		if !ok {
			return &fatalError{fmt.Errorf("representation %s disappeared from the manifest", ts.rep.ID)}
		}
		ts.rep = rep
	}
	return nil
}

func findRepresentation(pres *mpd.Presentation, id string) (mpd.Representation, bool) {
	if len(pres.Periods) == 0 {
		return mpd.Representation{}, false
	}
	for _, rep := range pres.Periods[0].Representations {
		if rep.ID == id {
			return rep, true
		}
	}
	return mpd.Representation{}, false
}

// advance publishes everything the refreshed manifest exposes beyond each
// track's position, re-anchoring first if the stream moved out from under us.
// It re-resolves the representations itself: a caller that forgot to would
// schedule against the previous manifest's timeline without any sign of it.
func (n *Native) advance(ctx context.Context, pres *mpd.Presentation) error {
	if err := n.refreshPlan(pres); err != nil {
		return err
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, ts := range []*trackState{n.video, n.audio} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = n.advanceTrack(ctx, pres, ts)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (n *Native) advanceTrack(ctx context.Context, pres *mpd.Presentation, ts *trackState) error {
	segs, err := pres.AvailableSegments(0, ts.rep, n.now())
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return nil
	}

	if pres.Dynamic {
		if reason, ok := n.needsReanchor(ts, segs); ok {
			if err := n.reanchor(ctx, ts, reason); err != nil {
				return err
			}
			return n.publishSegments(ctx, ts, n.startWindow(pres, segs))
		}
	}

	pending := make([]mpd.Segment, 0, len(segs))
	for _, seg := range segs {
		if !ts.hasLast || seg.Time > ts.lastTime {
			pending = append(pending, seg)
		}
	}
	return n.publishSegments(ctx, ts, pending)
}

// needsReanchor decides whether our idea of where we are in the stream is still
// valid. Both cases here are silent stalls if left undetected: the publication
// keeps refreshing the manifest and keeps publishing nothing.
func (n *Native) needsReanchor(ts *trackState, segs []mpd.Segment) (string, bool) {
	if !ts.hasLast {
		return "", false
	}
	// The origin's timeline moved backwards: a rollover, or a new origin whose
	// numbering restarted. Nothing ahead of our position will ever appear.
	newest := segs[len(segs)-1]
	if newest.Time < ts.lastTime {
		return "timeline_rollback", true
	}
	// The window has drifted entirely past us: everything on offer is older
	// than what we already published, and no new segment is arriving.
	if n.now().Sub(ts.lastProgress) > n.opts.ReanchorAfter {
		return "no_progress", true
	}
	return "", false
}

// reanchor drops our position, re-reads the init segment in case it changed,
// and marks the next publish as a discontinuity. The published sequence keeps
// counting up, so players never see it go backwards.
func (n *Native) reanchor(ctx context.Context, ts *trackState, reason string) error {
	n.mu.Lock()
	n.reanchors++
	count := n.reanchors
	n.mu.Unlock()
	n.log.Warn("live re-anchor", "track", ts.name, "reason", reason, "count", count)

	init, err := loadInit(ctx, n.opts, ts.rep)
	if err != nil {
		return err
	}
	if init.Track.Codec != ts.init.Track.Codec {
		return fmt.Errorf("codec changed from %s to %s on %s; cannot continue natively",
			ts.init.Track.Codec, init.Track.Codec, ts.rep.ID)
	}
	if init.Track.KID != ts.init.Track.KID {
		if _, ok := n.opts.Keys[init.Track.KID]; !ok {
			return fmt.Errorf("no key for the new kid %s on %s", init.Track.KID, ts.rep.ID)
		}
	}

	// A new init means a new EXT-X-MAP. Publishing it unconditionally would
	// churn the playlist, so only do it when the init actually changed.
	if !bytes.Equal(init.Clear, ts.init.Clear) {
		if err := n.pub.PublishInit(ts.name, init.Clear); err != nil {
			return err
		}
		n.log.Info("init segment changed", "track", ts.name)
	}
	ts.init = init
	ts.hasLast = false
	ts.hasExpected = false
	ts.forceDiscontinuity = true
	ts.lastProgress = n.now()
	return nil
}

func (n *Native) fail(err error) {
	n.mu.Lock()
	if n.err == nil {
		n.err = err
	}
	n.mu.Unlock()
}

// packMode is the native engine's internal mode. It is a different axis from
// Engine, and deliberately does not reuse ffmpeg's remote_live/local_filtered
// values, which are already exposed through the status API.
func packMode(pres *mpd.Presentation, plan Plan) string {
	kind := "static"
	if pres.Dynamic {
		kind = "dynamic"
	}
	switch plan.Video.Addressing.Mode {
	case mpd.AddressingTemplateTimeline:
		return kind + "_timeline"
	case mpd.AddressingTemplateDuration:
		return kind + "_duration"
	case mpd.AddressingList:
		return kind + "_list"
	default:
		return kind
	}
}

func (n *Native) Publication() *hls.Publisher { return n.pub }
func (n *Native) Engine() string              { return n.plan.Engine }
func (n *Native) PackMode() string            { return n.packMode }
func (n *Native) Done() <-chan struct{}       { return n.done }

// Reanchors is how many times the publication had to relocate the live edge.
func (n *Native) Reanchors() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.reanchors
}

func (n *Native) Err() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.err
}

// IntentionalStop separates "we stopped it" from "it died", which is what the
// session restart budget keys off.
func (n *Native) IntentionalStop() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.intentional
}

func (n *Native) Stop() error {
	n.mu.Lock()
	n.intentional = true
	n.mu.Unlock()
	n.cancel()
	<-n.done
	return nil
}
