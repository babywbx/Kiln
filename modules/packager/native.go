package packager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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

// audioTrackName names an audio rendition. The first one keeps a fixed name: it
// is the default rendition, and the name is part of the published URL, so it
// must not move when the source reorders its languages. The rest are named after
// the language they carry, which is what a player shows.
func audioTrackName(i int, rep mpd.Representation, taken map[string]struct{}) string {
	if i == 0 {
		return trackAudio
	}
	name := "audio-" + slug(rep.Lang)
	if slug(rep.Lang) == "" {
		name = "audio-" + slug(rep.ID)
	}
	if _, clash := taken[name]; clash || name == "audio-" {
		name = fmt.Sprintf("audio-%d", i+1)
	}
	return name
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return b.String()
}

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
	// PrimaryTrackHold bounds how far, in media time, audio may run ahead of
	// video. Audio and video keep their own segment boundaries, so they are never
	// paired by number, but audio must not run away: it would slide its own
	// playlist window past the segments a player stalled on video still needs.
	PrimaryTrackHold time.Duration
	// Gate bounds the segment bytes held in memory. It is shared across every
	// channel, so peak memory is a property of the process, not of how many
	// channels happen to be running. A nil Gate gets a private default one.
	Gate *byteGate
	Now  func() time.Time
	Log  *slog.Logger
}

const (
	defaultStartSegments    = 3
	defaultPrefetch         = 3
	defaultMaxSegmentBytes  = 32 << 20
	defaultReanchorAfter    = 30 * time.Second
	defaultPrimaryTrackHold = 12 * time.Second
	defaultRefreshInterval  = 2 * time.Second
	// defaultInflightBytes bounds the segment bytes held in memory across every
	// channel at once. It is the knob that decides peak memory, and it is set
	// for latency: measured on a 4K source, 96 MB is where time-to-first-playlist
	// stops improving, so a larger budget buys memory cost and nothing else.
	defaultInflightBytes = 96 << 20
	// initialSegmentEstimate is what a segment is assumed to cost before one has
	// been seen. It only affects the very first fetches.
	initialSegmentEstimate = 4 << 20
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
	if o.PrimaryTrackHold <= 0 {
		o.PrimaryTrackHold = defaultPrimaryTrackHold
	}
	if o.Gate == nil {
		o.Gate = newByteGate(defaultInflightBytes)
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

	// readyMillis is the presentation time this track is published through, on
	// the manifest's timeline. It is the watermark the other track reads, so it
	// is atomic; -1 means nothing is published yet.
	readyMillis atomic.Int64
	// segBytes is the last segment size seen on this track, which is what the
	// next one is budgeted against. Rewritten from the fetch goroutines.
	segBytes atomic.Int64
}

func newTrackState(name string, rep mpd.Representation, init *cmaf.Init, now time.Time) *trackState {
	ts := &trackState{name: name, rep: rep, init: init, nextSeq: 1, lastProgress: now}
	ts.readyMillis.Store(-1)
	return ts
}

// estimate is how much memory the next segment on this track will cost:
// ciphertext and plaintext are both live across the decrypt.
//
// Before a segment has been seen, the manifest already says: bandwidth times
// segment duration is the segment size. Guessing small here would be the whole
// bug it is meant to prevent, since every fetch of the opening window starts
// before any of them has reported a size.
func (ts *trackState) estimate(segmentSeconds float64) int64 {
	n := ts.segBytes.Load()
	if n <= 0 {
		n = declaredSize(ts.rep.Bandwidth, segmentSeconds)
	}
	return 2 * n
}

func declaredSize(bandwidth int, seconds float64) int64 {
	if bandwidth <= 0 || seconds <= 0 {
		return initialSegmentEstimate
	}
	n := int64(float64(bandwidth) / 8 * seconds)
	if n < initialSegmentEstimate {
		return initialSegmentEstimate
	}
	return n
}

func (ts *trackState) observe(size int) {
	if size > 0 {
		ts.segBytes.Store(int64(size))
	}
}

// Native is a running native_rewrite publication.
type Native struct {
	opts Options
	now  func() time.Time
	log  *slog.Logger

	pub      *hls.Publisher
	plan     Plan
	packMode string
	gate     *byteGate
	// refresh is the manifest URL to re-read. Only the run goroutine touches it.
	refresh string

	video *trackState
	// audios is every audio rendition the source offers, in manifest order. They
	// share the video's timeline but keep their own segment boundaries and their
	// own position in it.
	audios []*trackState

	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	err         error
	intentional bool

	segmentsPublished atomic.Uint64
	segmentsFetched   atomic.Uint64
	segmentFetchErrs  atomic.Uint64
	manifestRefreshes atomic.Uint64
	manifestErrs      atomic.Uint64
	discontinuities   atomic.Uint64
	reanchors         atomic.Uint64
	trackHolds        atomic.Uint64
	keyMismatches     atomic.Uint64
	decryptNanos      atomic.Int64
}

func (n *Native) Stats() Stats {
	bytes, items := n.pub.CacheUsage()
	frontier := n.pub.Frontier()
	return Stats{
		AudioTracks:       len(n.audios),
		SegmentsPublished: n.segmentsPublished.Load(),
		SegmentsFetched:   n.segmentsFetched.Load(),
		SegmentFetchErrs:  n.segmentFetchErrs.Load(),
		ManifestRefreshes: n.manifestRefreshes.Load(),
		ManifestErrs:      n.manifestErrs.Load(),
		Discontinuities:   n.discontinuities.Load(),
		Reanchors:         n.reanchors.Load(),
		TrackHolds:        n.trackHolds.Load(),
		KeyMismatches:     n.keyMismatches.Load(),
		DecryptSeconds:    time.Duration(n.decryptNanos.Load()).Seconds(),
		CacheBytes:        bytes,
		CacheItems:        items,
		VideoFrontier:     frontier[trackVideo],
		AudioFrontier:     frontier[trackAudio],
	}
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

	pres, err := fetchManifest(ctx, opts, opts.ManifestURL)
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

	videoInit, audioInits, err := fetchInits(ctx, opts, plan)
	if err != nil {
		return nil, err
	}
	audioTracks := make([]cmaf.Track, len(audioInits))
	for i, init := range audioInits {
		audioTracks[i] = init.Track
	}
	if err := VerifyTracks(&plan, videoInit.Track, audioTracks, opts.Keys); err != nil {
		return nil, &FallbackError{Reason: plan.Reason, Allowed: plan.FallbackAllowed, Err: err}
	}
	if len(plan.SkippedAudios) > 0 {
		opts.Log.Warn("audio renditions left out of the publication",
			"skipped", plan.SkippedAudios, "carried", len(plan.Audios))
	}

	pub, err := hls.New(hls.Config{
		Dir:                opts.Dir,
		PlaylistSize:       opts.PlaylistSize,
		Grace:              opts.Grace,
		Static:             !pres.Dynamic,
		MaxSegmentDuration: pres.MaxSegmentDuration,
		Now:                opts.Now,
	})
	if err != nil {
		return nil, noFallback(plan, err)
	}

	now := opts.Now()
	n := &Native{
		opts:     opts,
		now:      opts.Now,
		log:      opts.Log,
		pub:      pub,
		plan:     plan,
		packMode: packMode(pres, plan),
		gate:     opts.Gate,
		refresh:  pres.Refresh,
		done:     make(chan struct{}),
		video:    newTrackState(trackVideo, plan.Video, videoInit, now),
	}
	taken := map[string]struct{}{}
	for i, rep := range plan.Audios {
		name := audioTrackName(i, rep, taken)
		taken[name] = struct{}{}
		n.audios = append(n.audios, newTrackState(name, rep, audioInits[i], now))
	}
	if err := n.registerTracks(); err != nil {
		return nil, noFallback(plan, err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel

	if err := n.publishFirst(ctx, pres); err != nil {
		cancel()
		return nil, noFallback(plan, err)
	}

	go n.run(runCtx, pres)
	return n, nil
}

// noFallback re-stamps a startup failure with the plan's fallback verdict. Once
// the init segments prove the input is multi-KID, every later failure has to
// carry that: ffmpeg takes a single key, so a plain error here would send the
// stream down a path that starts and then decodes one track to garbage.
func noFallback(plan Plan, err error) error {
	if err == nil || plan.FallbackAllowed {
		return err
	}
	var fb *FallbackError
	if errors.As(err, &fb) {
		return &FallbackError{Reason: fb.Reason, Allowed: false, Err: fb.Err}
	}
	return &FallbackError{Reason: ReasonMultiKIDNoFall, Allowed: false, Err: err}
}

func fetchManifest(ctx context.Context, opts Options, url string) (*mpd.Presentation, error) {
	raw, finalURL, err := opts.Fetcher.Fetch(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	pres, err := mpd.Parse(raw, finalURL)
	if err != nil {
		return nil, &FallbackError{Reason: ReasonAddressing, Allowed: true, Err: err}
	}
	pres.Refresh = refreshURL(pres, finalURL)
	return pres, nil
}

// refreshURL is where the next manifest refresh must go. DASH says to use
// <Location> when the manifest carries one. Otherwise it is the URL the fetch
// actually resolved to, which matters more than it looks: an origin that
// load-balances by redirect hands out a different edge, and a different session,
// on every request. Re-resolving the entry URL each time would hop between edges
// whose timelines are not in step, and every hop reads as a rollover.
func refreshURL(pres *mpd.Presentation, finalURL string) string {
	if pres.Location != "" {
		return pres.Location
	}
	return finalURL
}

// fetchInits reads every track's init segment at once. They are small, and the
// publication cannot start until all of them are in.
func fetchInits(ctx context.Context, opts Options, plan Plan) (*cmaf.Init, []*cmaf.Init, error) {
	reps := append([]mpd.Representation{plan.Video}, plan.Audios...)
	inits := make([]*cmaf.Init, len(reps))
	errs := make([]error, len(reps))

	var wg sync.WaitGroup
	for i, rep := range reps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inits[i], errs[i] = loadInit(ctx, opts, rep)
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return nil, nil, err
	}

	if inits[0].Track.Kind != cmaf.KindVideo {
		return nil, nil, mismatchedInit()
	}
	for _, init := range inits[1:] {
		if init.Track.Kind != cmaf.KindAudio {
			return nil, nil, mismatchedInit()
		}
	}
	return inits[0], inits[1:], nil
}

func mismatchedInit() error {
	return &FallbackError{Reason: cmaf.ReasonSampleEntry, Allowed: true,
		Err: errors.New("init segments do not match the manifest content types")}
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
	for _, ts := range n.audios {
		if err := n.pub.AddTrack(hls.Track{
			Name:      ts.name,
			Kind:      hls.KindAudio,
			Codec:     ts.init.Track.Codec,
			Bandwidth: ts.rep.Bandwidth,
			Channels:  ts.rep.AudioChannels,
			Lang:      ts.rep.Lang,
			Label:     audioLabel(ts.rep),
		}); err != nil {
			return err
		}
	}
	for _, ts := range n.tracks() {
		if err := n.pub.PublishInit(ts.name, ts.init.Clear); err != nil {
			return err
		}
	}
	return nil
}

// tracks is every published track, video first. Audio is fanned out over the
// same code as video: they differ only in the hold, not in how they are carried.
func (n *Native) tracks() []*trackState {
	return append([]*trackState{n.video}, n.audios...)
}

// audioLabel is what a player shows in its audio menu.
func audioLabel(rep mpd.Representation) string {
	if rep.Lang != "" {
		return rep.Lang
	}
	return rep.ID
}

// publishFirst gets to playable without waiting for anything the origin has
// not published yet: it takes the opening window of segments that already
// exist and fetches them in parallel across both tracks.
func (n *Native) publishFirst(ctx context.Context, pres *mpd.Presentation) error {
	tracks := n.tracks()
	errs := make([]error, len(tracks))
	var wg sync.WaitGroup
	for i, ts := range tracks {
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

// prepared is a decrypted segment that is already on disk. It deliberately
// carries no media bytes: keeping a batch of decrypted 4K segments in memory
// until the last one arrives is what made a single channel cost a hundred
// megabytes.
type prepared struct {
	staged   string
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

	var err error
	for i, seg := range segs {
		if err != nil {
			// Everything after the first failure is dropped, so its staged file
			// would otherwise linger in the work directory forever.
			n.pub.Discard(results[i].staged)
			continue
		}
		if results[i].err != nil {
			err = results[i].err
			continue
		}
		err = n.commit(ts, seg, results[i])
	}
	return err
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

	if err := n.pub.PublishStaged(ts.name, ts.nextSeq, p.dur, p.staged, discontinuity); err != nil {
		return err
	}
	n.segmentsPublished.Add(1)
	if discontinuity {
		n.discontinuities.Add(1)
	}

	ts.nextSeq++
	ts.lastTime = seg.Time
	ts.hasLast = true
	ts.expectedDTS = p.baseTime + p.duration
	ts.hasExpected = true
	ts.forceDiscontinuity = false
	ts.lastProgress = n.now()
	ts.readyMillis.Store(millis(seg.Time+seg.Duration, ts.rep.Addressing.Timescale))
	return nil
}

func millis(ticks uint64, timescale uint64) int64 {
	if timescale == 0 {
		return -1
	}
	return int64(ticks * 1000 / timescale)
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

// prepare fetches, decrypts and puts one segment on disk. It holds the media in
// memory only for as long as it takes to decrypt it, and reserves that much of
// the process-wide budget first, so a 4K channel throttles itself instead of
// letting three fourteen-megabyte segments and their plaintext pile up at once.
func (n *Native) prepare(ctx context.Context, ts *trackState, seg mpd.Segment) (res prepared) {
	held, err := n.gate.acquire(ctx, ts.estimate(seg.Seconds(ts.rep.Addressing.Timescale)))
	if err != nil {
		res.err = err
		return res
	}
	defer n.gate.release(held)

	n.segmentsFetched.Add(1)
	raw, _, err := n.opts.Fetcher.Fetch(ctx, seg.URL)
	if err != nil {
		n.segmentFetchErrs.Add(1)
		res.err = fmt.Errorf("fetch segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}
	if n.opts.MaxSegmentBytes > 0 && int64(len(raw)) > n.opts.MaxSegmentBytes {
		n.segmentFetchErrs.Add(1)
		res.err = fmt.Errorf("segment %s#%d is %d bytes, over the limit", ts.rep.ID, seg.Number, len(raw))
		return res
	}
	ts.observe(len(raw))

	started := time.Now()
	clear, err := ts.init.Decrypt(raw, n.opts.Keys)
	n.decryptNanos.Add(int64(time.Since(started)))
	raw = nil
	if err != nil {
		if u, ok := cmaf.Unsupported(err); ok && u.Reason == cmaf.ReasonMissingKey {
			n.keyMismatches.Add(1)
		}
		res.err = fmt.Errorf("decrypt segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}

	res.baseTime = clear.BaseTime
	res.duration = clear.Duration
	res.dur = seg.Seconds(ts.rep.Addressing.Timescale)
	if clear.Duration > 0 && ts.init.Track.Timescale > 0 {
		res.dur = float64(clear.Duration) / float64(ts.init.Track.Timescale)
	}
	res.staged, err = n.pub.Stage(clear.Clear)
	if err != nil {
		res.err = err
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

		n.manifestRefreshes.Add(1)
		next, err := n.refreshManifest(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			n.manifestErrs.Add(1)
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

// refreshManifest stays on the manifest we are already following, and only goes
// back to the entry URL when that stops working. A pinned session can expire or
// its edge can fail; re-resolving then is the recovery, and the timeline jump it
// may bring is a real one, which the re-anchor handles.
func (n *Native) refreshManifest(ctx context.Context) (*mpd.Presentation, error) {
	pres, err := fetchManifest(ctx, n.opts, n.refresh)
	if err == nil {
		n.refresh = pres.Refresh
		return pres, nil
	}
	if ctx.Err() != nil || n.refresh == n.opts.ManifestURL {
		return nil, err
	}
	n.log.Warn("pinned manifest failed, re-resolving from the entry url",
		"pinned", n.refresh, "err", err)
	pres, err = fetchManifest(ctx, n.opts, n.opts.ManifestURL)
	if err != nil {
		return nil, err
	}
	n.refresh = pres.Refresh
	return pres, nil
}

// fatalError ends the publication instead of being retried on the next refresh.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

// refreshPlan re-resolves the chosen representations against the new manifest.
// Each track's addressing, including its timeline, lives on the representation,
// so skipping this would keep scheduling against a stale window.
func (n *Native) refreshPlan(pres *mpd.Presentation) error {
	for _, ts := range n.tracks() {
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
	tracks := n.tracks()
	errs := make([]error, len(tracks))
	var wg sync.WaitGroup
	for i, ts := range tracks {
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
	if ts != n.video {
		pending = n.holdBehindVideo(ts, pending)
	}
	return n.publishSegments(ctx, ts, pending)
}

// holdBehindVideo drops the audio segments that would run further ahead of the
// video watermark than the hold allows. They are not lost: nothing about the
// track's position moves, so the next manifest refresh offers them again.
//
// The hold cannot deadlock. It only engages while video is not advancing, and a
// video that stops advancing hits the no-progress re-anchor on its own, which
// relocates both tracks to the live edge.
func (n *Native) holdBehindVideo(ts *trackState, pending []mpd.Segment) []mpd.Segment {
	watermark := n.video.readyMillis.Load()
	if watermark < 0 || len(pending) == 0 {
		return pending
	}
	limit := watermark + n.opts.PrimaryTrackHold.Milliseconds()
	timescale := ts.rep.Addressing.Timescale
	for i, seg := range pending {
		if start := millis(seg.Time, timescale); start >= 0 && start > limit {
			n.trackHolds.Add(1)
			n.log.Info("holding audio behind the video watermark", "track", ts.name,
				"video_ready_ms", watermark, "audio_start_ms", start, "held", len(pending)-i)
			return pending[:i]
		}
	}
	return pending
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
	count := n.reanchors.Add(1)
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
func (n *Native) Reanchors() uint64 { return n.reanchors.Load() }

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
