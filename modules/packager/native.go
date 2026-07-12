package packager

import (
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
	Now             func() time.Time
	Log             *slog.Logger
}

const (
	defaultStartSegments   = 3
	defaultPrefetch        = 3
	defaultMaxSegmentBytes = 32 << 20
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
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
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

	cancel      context.CancelFunc
	done        chan struct{}
	mu          sync.Mutex
	err         error
	intentional bool
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
// init segments and the first media segment of each primary track are on disk.
// It does not wait for a second segment to prove health; that continues in the
// background.
func StartNative(ctx context.Context, opts Options) (*Native, error) {
	if opts.Fetcher == nil {
		return nil, errors.New("packager: no fetcher")
	}
	opts.applyDefaults()

	raw, finalURL, err := opts.Fetcher.Fetch(ctx, opts.ManifestURL)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	pres, err := mpd.Parse(raw, finalURL)
	if err != nil {
		return nil, &FallbackError{Reason: ReasonAddressing, Allowed: true, Err: err}
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

	n := &Native{
		opts:     opts,
		now:      opts.Now,
		log:      opts.Log,
		pub:      pub,
		plan:     plan,
		packMode: packMode(pres, plan),
		done:     make(chan struct{}),
	}
	if err := n.registerTracks(videoInit, audioInit); err != nil {
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel

	if err := n.publishFirst(ctx, pres, videoInit, audioInit); err != nil {
		cancel()
		return nil, err
	}

	go n.run(runCtx, pres, videoInit, audioInit)
	return n, nil
}

func fetchInits(ctx context.Context, opts Options, plan Plan) (*cmaf.Init, *cmaf.Init, error) {
	type result struct {
		init *cmaf.Init
		err  error
	}
	get := func(rep mpd.Representation) result {
		if rep.Addressing.InitURL == "" {
			return result{err: &FallbackError{Reason: ReasonAddressing, Allowed: true,
				Err: fmt.Errorf("representation %s has no initialization segment", rep.ID)}}
		}
		raw, _, err := opts.Fetcher.Fetch(ctx, rep.Addressing.InitURL)
		if err != nil {
			return result{err: fmt.Errorf("fetch init %s: %w", rep.ID, err)}
		}
		init, err := cmaf.ParseInit(raw)
		if err != nil {
			if u, ok := cmaf.Unsupported(err); ok {
				return result{err: &FallbackError{Reason: u.Reason, Allowed: true, Err: err}}
			}
			return result{err: fmt.Errorf("parse init %s: %w", rep.ID, err)}
		}
		return result{init: init}
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

func (n *Native) registerTracks(videoInit, audioInit *cmaf.Init) error {
	if err := n.pub.AddTrack(hls.Track{
		Name:      trackVideo,
		Kind:      hls.KindVideo,
		Codec:     videoInit.Track.Codec,
		Bandwidth: n.plan.Video.Bandwidth,
		Width:     n.plan.Video.Width,
		Height:    n.plan.Video.Height,
	}); err != nil {
		return err
	}
	if err := n.pub.AddTrack(hls.Track{
		Name:      trackAudio,
		Kind:      hls.KindAudio,
		Codec:     audioInit.Track.Codec,
		Bandwidth: n.plan.Audio.Bandwidth,
		Channels:  n.plan.Audio.AudioChannels,
		Lang:      n.plan.Audio.Lang,
	}); err != nil {
		return err
	}
	if err := n.pub.PublishInit(trackVideo, videoInit.Clear); err != nil {
		return err
	}
	return n.pub.PublishInit(trackAudio, audioInit.Clear)
}

// publishFirst gets to playable without waiting for anything the origin has
// not published yet: it takes the opening window of segments that already
// exist and fetches them in parallel across both tracks.
func (n *Native) publishFirst(ctx context.Context, pres *mpd.Presentation, videoInit, audioInit *cmaf.Init) error {
	video, err := pres.AvailableSegments(0, n.plan.Video, n.now())
	if err != nil {
		return err
	}
	audio, err := pres.AvailableSegments(0, n.plan.Audio, n.now())
	if err != nil {
		return err
	}
	if len(video) == 0 || len(audio) == 0 {
		return &FallbackError{Reason: ReasonAddressing, Allowed: true,
			Err: errors.New("no segment is available yet at the live edge")}
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs[0] = n.publishSegments(ctx, trackVideo, videoInit, n.plan.Video, n.startWindow(pres, video))
	}()
	go func() {
		defer wg.Done()
		errs[1] = n.publishSegments(ctx, trackAudio, audioInit, n.plan.Audio, n.startWindow(pres, audio))
	}()
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

// publishSegments fetches and decrypts in parallel but publishes strictly in
// sequence order: the frontier only moves forward, so an out-of-order publish
// would silently drop the segments behind it.
func (n *Native) publishSegments(ctx context.Context, name string, init *cmaf.Init, rep mpd.Representation, segs []mpd.Segment) error {
	if len(segs) == 0 {
		return nil
	}
	type result struct {
		data []byte
		dur  float64
		err  error
	}
	results := make([]result, len(segs))
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
			results[i] = n.prepare(ctx, init, rep, seg)
		}()
	}
	wg.Wait()

	for i, seg := range segs {
		if results[i].err != nil {
			return results[i].err
		}
		if err := n.pub.PublishSegment(name, seg.Number, results[i].dur, results[i].data, false); err != nil {
			return err
		}
	}
	return nil
}

func (n *Native) prepare(ctx context.Context, init *cmaf.Init, rep mpd.Representation, seg mpd.Segment) (res struct {
	data []byte
	dur  float64
	err  error
}) {
	raw, _, err := n.opts.Fetcher.Fetch(ctx, seg.URL)
	if err != nil {
		res.err = fmt.Errorf("fetch segment %s#%d: %w", rep.ID, seg.Number, err)
		return res
	}
	if n.opts.MaxSegmentBytes > 0 && int64(len(raw)) > n.opts.MaxSegmentBytes {
		res.err = fmt.Errorf("segment %s#%d is %d bytes, over the limit", rep.ID, seg.Number, len(raw))
		return res
	}
	clear, err := init.Decrypt(raw, n.opts.Keys)
	if err != nil {
		res.err = fmt.Errorf("decrypt segment %s#%d: %w", rep.ID, seg.Number, err)
		return res
	}
	res.data = clear.Clear
	res.dur = seg.Seconds(rep.Addressing.Timescale)
	if clear.Duration > 0 && init.Track.Timescale > 0 {
		res.dur = float64(clear.Duration) / float64(init.Track.Timescale)
	}
	return res
}

// run keeps the publication moving after it is playable. Static inputs simply
// drain; dynamic inputs re-read the manifest on its own update period, with no
// fixed-interval polling of our own.
func (n *Native) run(ctx context.Context, pres *mpd.Presentation, videoInit, audioInit *cmaf.Init) {
	defer close(n.done)

	if !pres.Dynamic {
		if err := n.drainStatic(ctx, pres, videoInit, audioInit); err != nil {
			n.fail(err)
		}
		return
	}

	interval := pres.MinimumUpdatePeriod
	if interval <= 0 {
		interval = 2 * time.Second
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		raw, finalURL, err := n.opts.Fetcher.Fetch(ctx, n.opts.ManifestURL)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			n.log.Warn("mpd refresh failed", "err", err)
			timer.Reset(interval)
			continue
		}
		next, err := mpd.Parse(raw, finalURL)
		if err != nil {
			n.log.Warn("mpd parse failed", "err", err)
			timer.Reset(interval)
			continue
		}
		if err := n.advance(ctx, next, videoInit, audioInit); err != nil {
			if ctx.Err() != nil {
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

// advance publishes whatever the refreshed manifest exposes beyond the
// frontier. Segments already published are skipped by the publisher itself.
func (n *Native) advance(ctx context.Context, pres *mpd.Presentation, videoInit, audioInit *cmaf.Init) error {
	frontier := n.pub.Frontier()
	pairs := []struct {
		name string
		init *cmaf.Init
		rep  mpd.Representation
	}{
		{trackVideo, videoInit, n.plan.Video},
		{trackAudio, audioInit, n.plan.Audio},
	}

	errs := make([]error, len(pairs))
	var wg sync.WaitGroup
	for i, p := range pairs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			segs, err := pres.AvailableSegments(0, p.rep, n.now())
			if err != nil {
				errs[i] = err
				return
			}
			pending := make([]mpd.Segment, 0, len(segs))
			for _, seg := range segs {
				if seg.Number > frontier[p.name] {
					pending = append(pending, seg)
				}
			}
			errs[i] = n.publishSegments(ctx, p.name, p.init, p.rep, pending)
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (n *Native) drainStatic(ctx context.Context, pres *mpd.Presentation, videoInit, audioInit *cmaf.Init) error {
	return n.advance(ctx, pres, videoInit, audioInit)
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
