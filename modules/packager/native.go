package packager

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/bits"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
	"github.com/babywbx/kiln/modules/subtitle"
	"github.com/babywbx/kiln/modules/timedmeta"
)

type Fetcher interface {
	Fetch(ctx context.Context, url string) (data []byte, finalURL string, err error)
}

type reservedFetcher interface {
	FetchReserved(ctx context.Context, url string, reserve func(int64) error) (data []byte, finalURL string, err error)
}

type manifestReservedFetcher interface {
	FetchManifestReserved(ctx context.Context, url string, reserve func(int64) error) (data []byte, finalURL string, err error)
}

const (
	trackVideo = "video-main"
	trackAudio = "audio-main"
)

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

func videoTrackName(i int, rep, primary mpd.Representation, taken map[string]struct{}) string {
	if rep.ID == primary.ID {
		return trackVideo
	}
	name := fmt.Sprintf("video-%dp", rep.Height)
	if rep.Height <= 0 {
		name = "video-" + slug(rep.ID)
	}
	if _, clash := taken[name]; clash || name == "video-" {
		name = fmt.Sprintf("video-variant-%d", i+1)
	}
	return name
}

func subtitleTrackName(i int, rep mpd.Representation, taken map[string]struct{}) string {
	language := subtitle.NormalizeLanguage(rep.Lang)
	name := "subtitle-" + slug(language.Tag)
	if _, clash := taken[name]; clash || name == "subtitle-" {
		name = fmt.Sprintf("subtitle-%d", i+1)
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
	ManifestURL             string
	Dir                     string
	Keys                    cmaf.KeySet
	Fetcher                 Fetcher
	PreferHeight            int
	PreferredAudioLanguages []string
	Selection               config.TrackSelection
	PlaylistSize            int
	LLHLS                   bool
	PartTarget              time.Duration

	StartSegments int

	Prefetch        int
	DecryptWorkers  int
	MaxSegmentBytes int64
	Grace           time.Duration

	ReanchorAfter time.Duration

	PrimaryTrackHold time.Duration

	StallTimeout time.Duration

	Gate         *byteGate
	InitPool     chan struct{}
	DownloadPool chan struct{}
	DecryptPool  chan struct{}
	Now          func() time.Time
	Log          *slog.Logger
}

const (
	defaultStartSegments    = 3
	defaultPrefetch         = 3
	defaultMaxSegmentBytes  = 32 << 20
	defaultMaxInitBytes     = 4 << 20
	defaultReanchorAfter    = 30 * time.Second
	defaultPrimaryTrackHold = 12 * time.Second
	defaultStallTimeout     = 3 * time.Minute
	defaultRefreshInterval  = 2 * time.Second

	defaultInflightBytes = 96 << 20

	initialSegmentEstimate = 4 << 20
)

var logURLPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

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
	if o.Gate == nil {
		o.Gate = newByteGate(defaultInflightBytes)
	}
	if o.DownloadPool == nil {
		o.DownloadPool = make(chan struct{}, downloadSlots(o.Gate.capacity(), o.MaxSegmentBytes))
	}
	if o.InitPool == nil {
		o.InitPool = make(chan struct{}, 1)
	}
	if o.DecryptPool == nil {
		if o.DecryptWorkers <= 0 {
			o.DecryptWorkers = decryptSlots(o.Gate.capacity(), o.MaxSegmentBytes)
		}
		o.DecryptPool = make(chan struct{}, o.DecryptWorkers)
	}
	if o.ReanchorAfter <= 0 {
		o.ReanchorAfter = defaultReanchorAfter
	}
	if o.PrimaryTrackHold <= 0 {
		o.PrimaryTrackHold = defaultPrimaryTrackHold
	}
	if o.StallTimeout == 0 {
		o.StallTimeout = defaultStallTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.LLHLS && o.PartTarget <= 0 {
		o.PartTarget = 500 * time.Millisecond
	}
}

type trackState struct {
	name     string
	rep      mpd.Representation
	init     *cmaf.Init
	initHash [sha256.Size]byte

	nextSeq          uint64
	nextMSN          uint64
	nextPartSequence uint64

	lastTime uint64
	hasLast  bool

	expectedDTS uint64
	hasExpected bool

	forceDiscontinuity bool
	lastProgress       time.Time

	readyMillis atomic.Int64

	segBytes atomic.Int64
}

func newTrackState(name string, rep mpd.Representation, init *cmaf.Init, now time.Time) *trackState {
	ts := &trackState{
		name: name, rep: rep, init: init, initHash: sha256.Sum256(init.Clear),
		nextSeq: 1, nextPartSequence: 1, lastProgress: now,
	}
	ts.readyMillis.Store(-1)
	return ts
}

func declaredSize(bandwidth int, seconds float64) int64 {
	if bandwidth <= 0 || seconds <= 0 {
		return initialSegmentEstimate
	}
	bytesPerSecond := int64(bandwidth) / 8
	if seconds >= float64(maxInt64)/float64(bytesPerSecond) {
		return maxInt64
	}
	n := int64(float64(bytesPerSecond) * seconds)
	if n < initialSegmentEstimate {
		return initialSegmentEstimate
	}
	return n
}

func segmentWorkingSet(n int64) int64 {
	return addBytes(n, n)
}

func addBytes(a, b int64) int64 {
	if a > maxInt64-b {
		return maxInt64
	}
	return a + b
}

const maxInt64 = int64(^uint64(0) >> 1)

func (ts *trackState) observe(size int) {
	if size > 0 {
		ts.segBytes.Store(int64(size))
	}
}

type Native struct {
	opts        Options
	now         func() time.Time
	sourceNow   func() time.Time
	clockOffset time.Duration
	log         *slog.Logger

	pub      *hls.Publisher
	plan     Plan
	packMode string
	gate     *byteGate
	download chan struct{}
	decrypt  chan struct{}

	budgetObserved func(string, int64)
	stagePrepare   func() error

	refresh string

	epoch time.Time

	lastManifestOK time.Time

	forceResolve atomic.Bool

	video  *trackState
	videos []*trackState

	audios []*trackState
	texts  []*trackState

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
	reresolves        atomic.Uint64
	trackHolds        atomic.Uint64
	keyMismatches     atomic.Uint64
	decryptNanos      atomic.Int64
	partsPublished    atomic.Uint64
}

func (n *Native) Stats() Stats {
	bytes, items := n.pub.CacheUsage()
	frontier := n.pub.Frontier()
	return Stats{
		VideoTracks:        len(n.videos),
		AudioTracks:        len(n.audios),
		TextTracks:         len(n.texts),
		ClockOffsetSeconds: n.clockOffset.Seconds(),
		SegmentsPublished:  n.segmentsPublished.Load(),
		PartsPublished:     n.partsPublished.Load(),
		SegmentsFetched:    n.segmentsFetched.Load(),
		SegmentFetchErrs:   n.segmentFetchErrs.Load(),
		ManifestRefreshes:  n.manifestRefreshes.Load(),
		ManifestErrs:       n.manifestErrs.Load(),
		Discontinuities:    n.discontinuities.Load(),
		Reanchors:          n.reanchors.Load(),
		Reresolves:         n.reresolves.Load(),
		TrackHolds:         n.trackHolds.Load(),
		KeyMismatches:      n.keyMismatches.Load(),
		DecryptSeconds:     time.Duration(n.decryptNanos.Load()).Seconds(),
		CacheBytes:         bytes,
		CacheItems:         items,
		VideoFrontier:      frontier[trackVideo],
		AudioFrontier:      frontier[trackAudio],
	}
}

type FallbackError struct {
	Reason  string
	Allowed bool
	Err     error
}

func (e *FallbackError) Error() string {
	return fmt.Sprintf("native unsupported (%s): %v", e.Reason, e.Err)
}
func (e *FallbackError) Unwrap() error { return e.Err }

func StartNative(ctx context.Context, opts Options) (*Native, error) {
	if opts.Fetcher == nil {
		return nil, errors.New("packager: no fetcher")
	}
	opts.applyDefaults()

	pres, err := fetchManifest(ctx, opts, opts.ManifestURL)
	if err != nil {
		return nil, err
	}
	if len(opts.Selection.Audio.PreferredLanguages) == 0 {
		opts.Selection.Audio.PreferredLanguages = append([]string(nil), opts.PreferredAudioLanguages...)
	}
	plan, err := PlanFromManifestWithSelection(pres, opts.PreferHeight, opts.Selection)
	if err != nil {
		return nil, err
	}
	if !plan.Native() {
		return nil, &FallbackError{Reason: plan.Reason, Allowed: plan.FallbackAllowed,
			Err: errors.New("manifest is outside the native support matrix")}
	}
	clockOffset, clockSource, clockErr := resolveClockOffset(ctx, pres.UTCTimings, opts.Fetcher, opts.Now, pres.BaseURL)
	if clockErr != nil {
		opts.Log.Warn("DASH UTC timing failed; using the local clock", "err", clockErr)
	}
	sourceNow := opts.Now
	if clockSource != "" {
		sourceNow = func() time.Time { return opts.Now().Add(clockOffset) }
		opts.Log.Info("DASH clock synchronized", "source", clockSource, "offset_ms", clockOffset.Milliseconds())
	}

	if !acquireSlot(ctx, opts.InitPool) {
		return nil, ctx.Err()
	}
	initSlotHeld := true
	defer func() {
		if initSlotHeld {
			<-opts.InitPool
		}
	}()

	videoInits, audioInits, textInits, initReservations, err := fetchInits(ctx, opts, plan)
	if err != nil {
		return nil, err
	}
	defer releaseReservations(initReservations)
	videoTracks := make([]cmaf.Track, len(videoInits))
	for i, init := range videoInits {
		videoTracks[i] = init.Track
	}
	audioTracks := make([]cmaf.Track, len(audioInits))
	for i, init := range audioInits {
		audioTracks[i] = init.Track
	}
	textTracks := make([]cmaf.Track, len(textInits))
	for i, init := range textInits {
		textTracks[i] = init.Track
	}
	if err := VerifyTrackSet(&plan, videoTracks, audioTracks, textTracks, opts.Keys); err != nil {
		return nil, &FallbackError{Reason: plan.Reason, Allowed: plan.FallbackAllowed, Err: err}
	}
	if len(plan.SkippedAudios) > 0 {
		opts.Log.Warn("audio renditions left out of the publication",
			"skipped", plan.SkippedAudios, "carried", len(plan.Audios))
	}
	if len(plan.SkippedText) > 0 {
		opts.Log.Warn("text renditions left out of the publication", "skipped", plan.SkippedText)
	}
	if len(plan.UnknownEssential) > 0 {
		opts.Log.Warn("carrying a representation with an essential property we do not act on",
			"schemes", plan.UnknownEssential)
	}
	maxSegmentDuration, err := publicationMaxSegmentDuration(pres, plan)
	if err != nil {
		return nil, noFallback(plan, err)
	}

	pub, err := hls.New(hls.Config{
		Dir:                opts.Dir,
		PlaylistSize:       opts.PlaylistSize,
		Grace:              opts.Grace,
		Static:             !pres.Dynamic,
		LLHLS:              opts.LLHLS,
		PartTarget:         opts.PartTarget,
		MaxSegmentDuration: maxSegmentDuration,
		Now:                opts.Now,
	})
	if err != nil {
		return nil, noFallback(plan, err)
	}

	now := opts.Now()
	n := &Native{
		opts:           opts,
		now:            opts.Now,
		sourceNow:      sourceNow,
		clockOffset:    clockOffset,
		log:            opts.Log,
		pub:            pub,
		plan:           plan,
		packMode:       packMode(pres, plan),
		gate:           opts.Gate,
		download:       opts.DownloadPool,
		decrypt:        opts.DecryptPool,
		refresh:        pres.Refresh,
		done:           make(chan struct{}),
		epoch:          presentationEpoch(pres),
		lastManifestOK: now,
	}
	taken := map[string]struct{}{}
	for i, rep := range plan.Videos {
		name := videoTrackName(i, rep, plan.Video, taken)
		taken[name] = struct{}{}
		track := newTrackState(name, rep, videoInits[i], now)
		n.videos = append(n.videos, track)
		if rep.ID == plan.Video.ID {
			n.video = track
		}
	}
	for i, rep := range plan.Audios {
		name := audioTrackName(i, rep, taken)
		taken[name] = struct{}{}
		n.audios = append(n.audios, newTrackState(name, rep, audioInits[i], now))
	}
	for i, rep := range plan.Texts {
		name := subtitleTrackName(i, rep, taken)
		taken[name] = struct{}{}
		n.texts = append(n.texts, newTrackState(name, rep, textInits[i], now))
	}
	if err := n.registerTracks(); err != nil {
		return nil, noFallback(plan, err)
	}
	for _, track := range n.tracks() {
		track.init.Clear = nil
	}
	releaseReservations(initReservations)
	<-opts.InitPool
	initSlotHeld = false

	runCtx, cancel := context.WithCancel(context.Background())
	n.cancel = cancel

	if err := n.publishFirst(ctx, pres); err != nil {
		cancel()
		return nil, noFallback(plan, err)
	}

	go n.run(runCtx, pres)
	return n, nil
}

func publicationMaxSegmentDuration(pres *mpd.Presentation, plan Plan) (time.Duration, error) {
	if pres.Dynamic {
		return pres.MaxSegmentDuration, nil
	}
	maximum := pres.MaxSegmentDuration
	representations := append([]mpd.Representation(nil), plan.Videos...)
	if len(representations) == 0 && plan.Video.ID != "" {
		representations = append(representations, plan.Video)
	}
	representations = append(representations, plan.Audios...)
	representations = append(representations, plan.Texts...)
	for _, rep := range representations {
		durations := []uint64{rep.Addressing.Duration}
		if rep.Addressing.Mode == mpd.AddressingTemplateTimeline {
			durations = make([]uint64, 0, len(rep.Addressing.Timeline))
			for _, entry := range rep.Addressing.Timeline {
				durations = append(durations, entry.Duration)
			}
		}
		for _, ticks := range durations {
			duration, ok := ticksDuration(ticks, rep.Addressing.Timescale)
			if !ok {
				return 0, fmt.Errorf("segment duration is outside the supported range on %s", rep.ID)
			}
			if duration > maximum {
				maximum = duration
			}
		}
	}
	return maximum, nil
}

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
	raw, finalURL, reservation, err := fetchManifestBytes(ctx, opts, url)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest: %w", err)
	}
	defer reservation.release()
	pres, err := mpd.Parse(raw, finalURL)
	if err != nil {
		return nil, &FallbackError{Reason: ReasonAddressing, Allowed: true, Err: err}
	}
	pres.Refresh = refreshURL(pres, finalURL)
	return pres, nil
}

func fetchManifestBytes(ctx context.Context, opts Options, url string) ([]byte, string, *byteReservation, error) {
	if !acquireSlot(ctx, opts.DownloadPool) {
		return nil, "", nil, ctx.Err()
	}
	reservation, err := opts.Gate.acquire(ctx, 1)
	if err != nil {
		<-opts.DownloadPool
		return nil, "", nil, err
	}
	reservation.onRelease = func() { <-opts.DownloadPool }

	var raw []byte
	var finalURL string
	if fetcher, ok := opts.Fetcher.(manifestReservedFetcher); ok {
		raw, finalURL, err = fetcher.FetchManifestReserved(ctx, url, func(liveBytes int64) error {
			if !reservation.resizeContext(ctx, liveBytes) {
				return errors.New("manifest memory budget exhausted")
			}
			return nil
		})
	} else {
		if !reservation.resizeContext(ctx, opts.MaxSegmentBytes) {
			reservation.release()
			return nil, "", nil, errors.New("manifest memory budget exhausted")
		}
		raw, finalURL, err = opts.Fetcher.Fetch(ctx, url)
	}
	if err != nil {
		reservation.release()
		return nil, "", nil, err
	}
	if int64(len(raw)) > opts.MaxSegmentBytes || int64(cap(raw)) > downloadWorkingSet(opts.MaxSegmentBytes) {
		reservation.release()
		return nil, "", nil, errors.New("upstream response too large")
	}
	if !reservation.resizeContext(ctx, int64(cap(raw))) {
		reservation.release()
		return nil, "", nil, errors.New("manifest memory budget exhausted")
	}
	return raw, finalURL, reservation, nil
}

func refreshURL(pres *mpd.Presentation, finalURL string) string {
	if pres.Location != "" {
		return pres.Location
	}
	return finalURL
}

func fetchInits(ctx context.Context, opts Options, plan Plan) ([]*cmaf.Init, []*cmaf.Init, []*cmaf.Init, []*byteReservation, error) {
	reps := append([]mpd.Representation(nil), plan.Videos...)
	reps = append(reps, plan.Audios...)
	reps = append(reps, plan.Texts...)
	inits := make([]*cmaf.Init, len(reps))
	reservations := make([]*byteReservation, len(reps))
	errs := make([]error, len(reps))
	limit := initByteLimit(opts, len(reps))

	jobs := make(chan int)
	var wg sync.WaitGroup
	workers := min(2, len(reps))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				inits[i], reservations[i], errs[i] = loadInit(ctx, opts, reps[i], limit)
			}
		}()
	}
	for i := range reps {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		releaseReservations(reservations)
		return nil, nil, nil, nil, err
	}

	videoEnd := len(plan.Videos)
	audioEnd := videoEnd + len(plan.Audios)
	for _, init := range inits[:videoEnd] {
		if init.Track.Kind != cmaf.KindVideo {
			releaseReservations(reservations)
			return nil, nil, nil, nil, mismatchedInit()
		}
	}
	for _, init := range inits[videoEnd:audioEnd] {
		if init.Track.Kind != cmaf.KindAudio {
			releaseReservations(reservations)
			return nil, nil, nil, nil, mismatchedInit()
		}
	}
	for _, init := range inits[audioEnd:] {
		if init.Track.Kind != cmaf.KindText {
			releaseReservations(reservations)
			return nil, nil, nil, nil, mismatchedInit()
		}
	}
	return inits[:videoEnd], inits[videoEnd:audioEnd], inits[audioEnd:], reservations, nil
}

func initByteLimit(opts Options, count int) int64 {
	limit := min(opts.MaxSegmentBytes, int64(defaultMaxInitBytes))
	if count > 0 {
		limit = min(limit, opts.Gate.capacity()/int64(count+2))
	}
	return max(limit, 1)
}

func releaseReservations(reservations []*byteReservation) {
	for _, reservation := range reservations {
		if reservation != nil {
			reservation.release()
		}
	}
}

func mismatchedInit() error {
	return &FallbackError{Reason: cmaf.ReasonSampleEntry, Allowed: true,
		Err: errors.New("init segments do not match the manifest content types")}
}

func loadInit(ctx context.Context, opts Options, rep mpd.Representation, limit int64) (*cmaf.Init, *byteReservation, error) {
	if rep.Addressing.InitURL == "" {
		return nil, nil, &FallbackError{Reason: ReasonAddressing, Allowed: true,
			Err: fmt.Errorf("representation %s has no initialization segment", rep.ID)}
	}
	raw, _, reservation, err := fetchBounded(ctx, opts.Fetcher, opts.Gate, opts.DownloadPool,
		limit, rep.Addressing.InitURL)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch init %s: %w", rep.ID, err)
	}
	reservation.releaseCallback()
	if !reservation.resizeContext(ctx, addBytes(int64(cap(raw)), limit)) {
		reservation.release()
		return nil, nil, fmt.Errorf("parse init %s: init memory budget exhausted", rep.ID)
	}
	init, err := cmaf.ParseInit(raw)
	if err != nil {
		reservation.release()
		if u, ok := cmaf.Unsupported(err); ok {
			return nil, nil, &FallbackError{Reason: u.Reason, Allowed: true, Err: err}
		}
		return nil, nil, fmt.Errorf("parse init %s: %w", rep.ID, err)
	}
	if int64(len(init.Clear)) > limit {
		reservation.release()
		return nil, nil, fmt.Errorf("parse init %s: clear init is too large", rep.ID)
	}
	clearBytes := int64(cap(init.Clear))
	if !reservation.resizeContext(ctx, addBytes(int64(cap(raw)), clearBytes)) {
		reservation.release()
		return nil, nil, fmt.Errorf("parse init %s: init memory budget exhausted", rep.ID)
	}
	if cap(init.Clear) != len(init.Clear) {
		peak := addBytes(addBytes(int64(cap(raw)), clearBytes), int64(len(init.Clear)))
		if !reservation.resizeContext(ctx, peak) {
			reservation.release()
			return nil, nil, fmt.Errorf("parse init %s: init memory budget exhausted", rep.ID)
		}
		exact := make([]byte, len(init.Clear))
		copy(exact, init.Clear)
		init.Clear = exact
	}
	reservation.shrink(int64(cap(init.Clear)))
	return init, reservation, nil
}

func (n *Native) registerTracks() error {
	for _, ts := range n.videos {
		if err := n.pub.AddTrack(hls.Track{
			Name:      ts.name,
			Kind:      hls.KindVideo,
			Codec:     ts.init.Track.Codec,
			Bandwidth: ts.rep.Bandwidth,
			Width:     ts.rep.Width,
			Height:    ts.rep.Height,
			FrameRate: frameRateValue(ts.rep.FrameRate),
		}); err != nil {
			return err
		}
	}
	languages := make([]string, len(n.audios))
	for i, ts := range n.audios {
		languages[i] = ts.rep.Lang
	}
	preferred := preferredAudioIndex(languages, n.opts.PreferredAudioLanguages)
	for i, ts := range n.audios {
		isDefault := n.plan.DefaultAudioKey != "" && trackIdentity(ts.rep) == n.plan.DefaultAudioKey
		if n.plan.DefaultAudioKey == "" {
			isDefault = i == preferred
		}
		if err := n.pub.AddTrack(hls.Track{
			Name:      ts.name,
			Kind:      hls.KindAudio,
			Codec:     ts.init.Track.Codec,
			Bandwidth: ts.rep.Bandwidth,
			Channels:  ts.rep.AudioChannels,
			Lang:      ts.rep.Lang,
			Label:     audioLabel(ts.rep),
			Default:   isDefault,
		}); err != nil {
			return err
		}
	}
	for _, ts := range n.texts {
		language := subtitle.NormalizeLanguage(ts.rep.Lang)
		if err := n.pub.AddTrack(hls.Track{
			Name: ts.name, Kind: hls.KindSubtitle,
			Lang: language.Tag, Label: language.Name,
			Default: n.plan.DefaultTextKey != "" && trackIdentity(ts.rep) == n.plan.DefaultTextKey,
			Forced:  hasRole(ts.rep, "forced"),
		}); err != nil {
			return err
		}
	}
	for _, ts := range n.tracks() {
		if ts.init.Track.Kind == cmaf.KindText {
			continue
		}
		if err := n.pub.PublishInit(ts.name, ts.init.Clear); err != nil {
			return err
		}
	}
	return nil
}

func (n *Native) tracks() []*trackState {
	tracks := append([]*trackState(nil), n.videos...)
	tracks = append(tracks, n.audios...)
	return append(tracks, n.texts...)
}

func audioLabel(rep mpd.Representation) string {
	if rep.Lang != "" {
		return rep.Lang
	}
	return rep.ID
}

func (n *Native) publishFirst(ctx context.Context, pres *mpd.Presentation) error {
	tracks := append([]*trackState(nil), n.videos...)
	tracks = append(tracks, n.audios...)
	errs := make([]error, len(tracks))
	var wg sync.WaitGroup
	for i, ts := range tracks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			segs, err := pres.AvailableSegments(0, ts.rep, n.sourceNow())
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

type pipelineSlot struct {
	seg  mpd.Segment
	done chan prepared
}

type prepared struct {
	staged   string
	dur      float64
	baseTime uint64
	duration uint64
	events   []cmaf.EventMessage
	parts    []preparedPart
	err      error
}

type preparedPart struct {
	staged      string
	duration    float64
	independent bool
}

func (n *Native) publishSegments(ctx context.Context, ts *trackState, segs []mpd.Segment) error {
	if len(segs) == 0 {
		return nil
	}
	pipelineCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	window := n.opts.Prefetch
	if window > len(segs) {
		window = len(segs)
	}
	slots := make([]pipelineSlot, 0, window)
	next := 0
	start := func() {
		slot := pipelineSlot{seg: segs[next], done: make(chan prepared, 1)}
		next++
		slots = append(slots, slot)
		go func() { slot.done <- n.prepare(pipelineCtx, ts, slot.seg) }()
	}
	for len(slots) < window {
		start()
	}

	for len(slots) > 0 {
		slot := slots[0]
		result := <-slot.done
		if result.err == nil {
			result.err = n.commit(ts, slot.seg, result)
		}
		if result.err != nil {
			cancel()
			n.discardPrepared(result)
			for _, pending := range slots[1:] {
				n.discardPrepared(<-pending.done)
			}
			return result.err
		}
		slots = slots[1:]
		if next < len(segs) {
			start()
		}
	}
	return nil
}

func (n *Native) discardPrepared(result prepared) {
	n.pub.Discard(result.staged)
	for _, part := range result.parts {
		n.pub.Discard(part.staged)
	}
}

func presentationEpoch(pres *mpd.Presentation) time.Time {
	if !pres.Dynamic || pres.AvailabilityStartTime.IsZero() || len(pres.Periods) == 0 {
		return time.Time{}
	}
	return pres.AvailabilityStartTime.Add(pres.Periods[0].Start)
}

func (n *Native) segmentTime(ts *trackState, seg mpd.Segment) time.Time {
	addr := ts.rep.Addressing
	if n.epoch.IsZero() || addr.Timescale == 0 || seg.Time < addr.PresentationTimeOffset {
		return time.Time{}
	}
	elapsed, ok := ticksDuration(seg.Time-addr.PresentationTimeOffset, addr.Timescale)
	if !ok {
		return time.Time{}
	}
	return n.epoch.Add(elapsed)
}

func (n *Native) commit(ts *trackState, seg mpd.Segment, p prepared) error {
	readyTicks, ok := addUint64(seg.Time, seg.Duration)
	if !ok {
		return fmt.Errorf("segment media time overflows on %s", ts.rep.ID)
	}
	readyMillis, ok := millis(readyTicks, ts.rep.Addressing.Timescale)
	if !ok {
		return fmt.Errorf("segment media time is outside the supported range on %s", ts.rep.ID)
	}
	expectedDTS, ok := addUint64(p.baseTime, p.duration)
	if !ok {
		return fmt.Errorf("decoded media time overflows on %s", ts.rep.ID)
	}

	discontinuity := ts.forceDiscontinuity
	if ts.hasExpected && !continuous(ts.expectedDTS, p.baseTime, ts.init.Track.Timescale) {
		discontinuity = true
		n.log.Info("timeline discontinuity",
			"track", ts.name, "expected_dts", ts.expectedDTS, "got_dts", p.baseTime)
	}

	for index, part := range p.parts {
		if ts.nextPartSequence > math.MaxUint32 {
			return fmt.Errorf("LL-HLS part sequence exhausted on %s", ts.rep.ID)
		}
		if err := rewriteStagedPartSequence(part.staged, uint32(ts.nextPartSequence)); err != nil {
			return fmt.Errorf("number LL-HLS part on %s: %w", ts.rep.ID, err)
		}
		if err := n.pub.PublishPartStaged(hls.PartPublication{
			Track: ts.name, MSN: ts.nextMSN, Part: uint64(index),
			Duration: part.duration, Independent: part.independent,
		}, part.staged); err != nil {
			return err
		}
		ts.nextPartSequence++
		n.partsPublished.Add(1)
	}
	pub := hls.Publication{
		Track:         ts.name,
		Seq:           ts.nextSeq,
		Duration:      p.dur,
		At:            n.segmentTime(ts, seg),
		Discontinuity: discontinuity,
		DateRanges:    n.dateRanges(ts, seg, p),
	}
	if err := n.pub.PublishStaged(pub, p.staged); err != nil {
		return err
	}
	ts.nextMSN++
	n.segmentsPublished.Add(1)
	if discontinuity {
		n.discontinuities.Add(1)
	}

	ts.nextSeq++
	ts.lastTime = seg.Time
	ts.hasLast = true
	ts.expectedDTS = expectedDTS
	ts.hasExpected = true
	ts.forceDiscontinuity = false
	ts.lastProgress = n.now()
	ts.readyMillis.Store(readyMillis)
	return nil
}

func rewriteStagedPartSequence(path string, sequence uint32) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := cmaf.RewritePartSequence(file, sequence); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (n *Native) dateRanges(ts *trackState, seg mpd.Segment, prepared prepared) []timedmeta.DateRange {
	if ts != n.video || len(prepared.events) == 0 {
		return nil
	}
	anchor := timedmeta.ClockAnchor{
		WallClock:        n.segmentTime(ts, seg),
		PresentationTime: prepared.baseTime,
		TimeScale:        ts.init.Track.Timescale,
	}
	if anchor.WallClock.IsZero() || anchor.TimeScale == 0 {
		return nil
	}
	var ranges []timedmeta.DateRange
	for _, message := range prepared.events {
		event, err := timedmeta.FromEmsg(timedmeta.Emsg{
			Version: message.Version, TimeScale: message.Timescale,
			PresentationTimeDelta: message.PresentationTimeDelta,
			PresentationTime:      message.PresentationTime,
			EventDuration:         message.EventDuration, ID: message.ID,
			SchemeIDURI: message.SchemeIDURI, Value: message.Value,
			MessageData: message.MessageData, SegmentPresentationTime: prepared.baseTime,
		})
		if err != nil {
			n.log.Warn("timed metadata ignored", "track", ts.name, "err", err)
			continue
		}
		dateRange, ok, err := event.DateRange(anchor)
		if err != nil {
			n.log.Warn("timed metadata clock mapping failed", "track", ts.name, "err", err)
			continue
		}
		if ok {
			ranges = append(ranges, dateRange)
		}
	}
	return ranges
}

func addUint64(a, b uint64) (uint64, bool) {
	if b > math.MaxUint64-a {
		return 0, false
	}
	return a + b, true
}

func millis(ticks uint64, timescale uint64) (int64, bool) {
	if timescale == 0 {
		return 0, false
	}
	whole := ticks / timescale
	if whole > math.MaxInt64/1000 {
		return 0, false
	}
	hi, lo := bits.Mul64(ticks%timescale, 1000)
	fraction, _ := bits.Div64(hi, lo, timescale)
	value := whole*1000 + fraction
	if value > math.MaxInt64 {
		return 0, false
	}
	return int64(value), true
}

func ticksDuration(ticks uint64, timescale uint64) (time.Duration, bool) {
	if timescale == 0 {
		return 0, false
	}
	whole := ticks / timescale
	if whole > math.MaxInt64/uint64(time.Second) {
		return 0, false
	}
	hi, lo := bits.Mul64(ticks%timescale, uint64(time.Second))
	fraction, _ := bits.Div64(hi, lo, timescale)
	value := whole*uint64(time.Second) + fraction
	if value > math.MaxInt64 {
		return 0, false
	}
	return time.Duration(value), true
}

func continuous(expected, got uint64, timescale uint32) bool {
	if timescale == 0 {
		return expected == got
	}
	tolerance := uint64(timescale) / 100
	if got >= expected {
		return got-expected <= tolerance
	}
	return expected-got <= tolerance
}

func (n *Native) observeBudget(phase string) {
	if n.budgetObserved != nil {
		n.budgetObserved(phase, n.gate.usage())
	}
}

func acquireSlot(ctx context.Context, pool chan struct{}) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case pool <- struct{}{}:
		if ctx.Err() != nil {
			<-pool
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func downloadWorkingSet(maxSegment int64) int64 {
	if maxSegment <= 0 {
		return 1
	}
	next := min(maxSegment, 32<<10)
	var previous int64
	for next < maxSegment {
		previous = next
		if next > maxSegment/2 {
			next = maxSegment
		} else {
			next *= 2
		}
	}
	return addBytes(previous, next)
}

func downloadSlots(budget, maxSegment int64) int {
	peak := downloadWorkingSet(maxSegment)
	slots := int(budget / peak)
	if budget > maxSegment && maxSegment > 0 {
		withDecryptReserve := int((budget - maxSegment) / maxSegment)
		if withDecryptReserve < slots {
			slots = withDecryptReserve
		}
	}
	if slots < 1 {
		return 1
	}
	return slots
}

func decryptSlots(budget, maxSegment int64) int {
	workingSet := segmentWorkingSet(maxSegment)
	if workingSet <= 0 {
		return 1
	}
	slots := int(budget / workingSet)
	if slots < 1 {
		return 1
	}
	return slots
}

func (n *Native) downloadPool() chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.download == nil {
		n.download = make(chan struct{}, downloadSlots(n.gate.capacity(), n.opts.MaxSegmentBytes))
	}
	return n.download
}

func (n *Native) fetchSegment(ctx context.Context, url string) ([]byte, *byteReservation, error) {
	raw, _, reservation, err := fetchBounded(ctx, n.opts.Fetcher, n.gate, n.downloadPool(),
		n.opts.MaxSegmentBytes, url)
	return raw, reservation, err
}

func fetchBounded(ctx context.Context, fetcher Fetcher, gate *byteGate, pool chan struct{}, maxBytes int64, url string) ([]byte, string, *byteReservation, error) {
	if !acquireSlot(ctx, pool) {
		return nil, "", nil, ctx.Err()
	}
	reservation, err := gate.acquire(ctx, 1)
	if err != nil {
		<-pool
		return nil, "", nil, err
	}
	reservation.onRelease = func() { <-pool }
	var raw []byte
	var finalURL string
	if reserved, ok := fetcher.(reservedFetcher); ok {
		raw, finalURL, err = reserved.FetchReserved(ctx, url, func(liveBytes int64) error {
			if liveBytes > downloadWorkingSet(maxBytes) {
				return errors.New("upstream response too large")
			}
			if !reservation.resizeContext(ctx, liveBytes) {
				return errors.New("segment memory budget exhausted")
			}
			return nil
		})
	} else {
		if !reservation.resizeContext(ctx, maxBytes) {
			reservation.release()
			return nil, "", nil, errors.New("segment memory budget exhausted")
		}
		raw, finalURL, err = fetcher.Fetch(ctx, url)
	}
	if err != nil {
		reservation.release()
		return nil, "", nil, err
	}
	if int64(len(raw)) > maxBytes || int64(cap(raw)) > addBytes(maxBytes, maxBytes) {
		reservation.release()
		return nil, "", nil, errors.New("upstream response too large")
	}
	if int64(cap(raw)) > maxBytes {
		peak := addBytes(int64(cap(raw)), int64(len(raw)))
		if !reservation.resizeContext(ctx, peak) {
			reservation.release()
			return nil, "", nil, errors.New("segment memory budget exhausted")
		}
		exact := make([]byte, len(raw))
		copy(exact, raw)
		raw = exact
	}
	if !reservation.resizeContext(ctx, int64(cap(raw))) {
		reservation.release()
		return nil, "", nil, errors.New("segment memory budget exhausted")
	}
	return raw, finalURL, reservation, nil
}

func (n *Native) acquireDecrypt(ctx context.Context) chan struct{} {
	n.mu.Lock()
	if n.decrypt == nil {
		n.decrypt = make(chan struct{}, defaultPrefetch)
	}
	pool := n.decrypt
	n.mu.Unlock()
	if ctx.Err() != nil {
		return nil
	}
	select {
	case pool <- struct{}{}:
		if ctx.Err() != nil {
			<-pool
			return nil
		}
		return pool
	case <-ctx.Done():
		return nil
	}
}

func (n *Native) prepare(ctx context.Context, ts *trackState, seg mpd.Segment) (res prepared) {
	n.segmentsFetched.Add(1)
	raw, reservation, err := n.fetchSegment(ctx, seg.URL)
	if err != nil {
		n.segmentFetchErrs.Add(1)
		res.err = fmt.Errorf("fetch segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}
	defer reservation.release()
	n.observeBudget("ciphertext")
	if n.opts.MaxSegmentBytes > 0 && int64(len(raw)) > n.opts.MaxSegmentBytes {
		n.segmentFetchErrs.Add(1)
		res.err = fmt.Errorf("segment %s#%d is %d bytes, over the limit", ts.rep.ID, seg.Number, len(raw))
		return res
	}
	reservation.shrink(int64(cap(raw)))
	ts.observe(len(raw))
	if ts.init.Track.Kind == cmaf.KindText {
		return n.prepareText(ctx, ts, seg, raw, reservation)
	}

	pool := n.acquireDecrypt(ctx)
	if pool == nil {
		res.err = ctx.Err()
		return res
	}
	if !n.opts.LLHLS {
		if !reservation.resizeContext(ctx, segmentWorkingSet(int64(cap(raw)))) {
			<-pool
			res.err = errors.New("segment memory budget exhausted")
			return res
		}
		var clear *cmaf.Segment
		var decryptErr error
		res.staged, err = n.pub.StageWrite(func(dst io.Writer) error {
			clear, decryptErr = ts.init.DecryptOwnedTo(raw, n.opts.Keys, dst)
			if decryptErr != nil {
				return decryptErr
			}
			if n.stagePrepare != nil {
				return n.stagePrepare()
			}
			return nil
		})
		<-pool
		if clear != nil {
			n.decryptNanos.Add(int64(clear.DecryptDuration))
		}
		if decryptErr != nil {
			if unsupported, ok := cmaf.Unsupported(decryptErr); ok && unsupported.Reason == cmaf.ReasonMissingKey {
				n.keyMismatches.Add(1)
			}
			res.err = fmt.Errorf("decrypt segment %s#%d: %w", ts.rep.ID, seg.Number, decryptErr)
			return res
		}
		if err != nil {
			res.err = err
			return res
		}
		n.observeBudget("plaintext")
		res.baseTime = clear.BaseTime
		res.duration = clear.Duration
		res.events = clear.Events
		res.dur = seg.Seconds(ts.rep.Addressing.Timescale)
		if clear.Duration > 0 && ts.init.Track.Timescale > 0 {
			res.dur = float64(clear.Duration) / float64(ts.init.Track.Timescale)
		}
		reservation.release()
		n.observeBudget("staged")
		return res
	}
	clear, err := ts.init.DecryptOwnedReserved(raw, n.opts.Keys, func(clearCapacity int64) error {
		if !reservation.resizeContext(ctx, addBytes(int64(cap(raw)), clearCapacity)) {
			return errors.New("segment memory budget exhausted")
		}
		return nil
	})
	<-pool
	if clear != nil {
		n.decryptNanos.Add(int64(clear.DecryptDuration))
	}
	if err != nil {
		if u, ok := cmaf.Unsupported(err); ok && u.Reason == cmaf.ReasonMissingKey {
			n.keyMismatches.Add(1)
		}
		res.err = fmt.Errorf("decrypt segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}
	n.observeBudget("plaintext")

	res.baseTime = clear.BaseTime
	res.duration = clear.Duration
	res.events = clear.Events
	res.dur = seg.Seconds(ts.rep.Addressing.Timescale)
	if clear.Duration > 0 && ts.init.Track.Timescale > 0 {
		res.dur = float64(clear.Duration) / float64(ts.init.Track.Timescale)
	}
	if n.opts.LLHLS {
		res.parts = n.prepareParts(ctx, ts, raw, clear.Clear, reservation)
	}
	if n.stagePrepare != nil {
		if err := n.stagePrepare(); err != nil {
			res.err = err
			return res
		}
	}
	res.staged, err = n.pub.Stage(clear.Clear)
	reservation.release()
	n.observeBudget("staged")
	if err != nil {
		res.err = err
	}
	return res
}

func (n *Native) prepareParts(ctx context.Context, ts *trackState, raw, clear []byte, reservation *byteReservation) []preparedPart {
	if ts.init.Track.Kind == cmaf.KindText || n.opts.PartTarget <= 0 {
		return nil
	}
	peak := addBytes(int64(cap(raw)), addBytes(int64(cap(clear)), addBytes(int64(len(clear)), int64(len(clear)))))
	if peak > n.gate.capacity() || !reservation.resizeContext(ctx, peak) {
		n.log.Warn("LL-HLS parts skipped for memory budget", "track", ts.name, "segment_bytes", len(clear))
		return nil
	}
	parts, err := ts.init.SplitParts(clear, n.opts.PartTarget)
	if err != nil {
		n.log.Warn("LL-HLS part split failed; publishing whole segment", "track", ts.name, "err", err)
		return nil
	}
	prepared := make([]preparedPart, 0, len(parts))
	for index := range parts {
		duration := float64(parts[index].Duration) / float64(ts.init.Track.Timescale)
		if duration <= 0 {
			continue
		}
		staged, stageErr := n.pub.Stage(parts[index].Data)
		parts[index].Data = nil
		if stageErr != nil {
			for _, part := range prepared {
				n.pub.Discard(part.staged)
			}
			n.log.Warn("LL-HLS part staging failed; publishing whole segment", "track", ts.name, "err", stageErr)
			return nil
		}
		prepared = append(prepared, preparedPart{
			staged: staged, duration: duration, independent: parts[index].Independent,
		})
	}
	return prepared
}

func (n *Native) prepareText(ctx context.Context, ts *trackState, seg mpd.Segment, raw []byte, reservation *byteReservation) (res prepared) {
	if !reservation.resizeContext(ctx, addBytes(int64(cap(raw)), int64(len(raw)))) {
		res.err = errors.New("text segment memory budget exhausted")
		return res
	}
	pool := n.acquireDecrypt(ctx)
	if pool == nil {
		res.err = ctx.Err()
		return res
	}
	started := time.Now()
	decoded, err := ts.init.DecodeText(raw, n.opts.Keys)
	<-pool
	n.decryptNanos.Add(int64(time.Since(started)))
	if err != nil {
		res.err = fmt.Errorf("decode text segment %s#%d: %w", ts.rep.ID, seg.Number, err)
		return res
	}
	if decoded.Timescale == 0 {
		res.err = fmt.Errorf("decode text segment %s#%d: zero timescale", ts.rep.ID, seg.Number)
		return res
	}
	windowStart, ok := ticksDuration(decoded.BaseTime, uint64(decoded.Timescale))
	if !ok {
		res.err = fmt.Errorf("decode text segment %s#%d: start time overflows", ts.rep.ID, seg.Number)
		return res
	}
	windowDuration, ok := ticksDuration(decoded.Duration, uint64(decoded.Timescale))
	if !ok || windowDuration <= 0 {
		windowDuration, ok = ticksDuration(seg.Duration, ts.rep.Addressing.Timescale)
	}
	if !ok || windowDuration <= 0 || windowStart > time.Duration(math.MaxInt64)-windowDuration {
		res.err = fmt.Errorf("decode text segment %s#%d: invalid duration", ts.rep.ID, seg.Number)
		return res
	}
	var cues []subtitle.Cue
	for _, sample := range decoded.Samples {
		if sample.PresentationTime < 0 {
			continue
		}
		decodeTime, decodeOK := ticksDuration(sample.DecodeTime, uint64(decoded.Timescale))
		start, startOK := ticksDuration(uint64(sample.PresentationTime), uint64(decoded.Timescale))
		duration, durationOK := ticksDuration(uint64(sample.Duration), uint64(decoded.Timescale))
		if !decodeOK || !startOK || !durationOK || duration <= 0 || start > time.Duration(math.MaxInt64)-duration {
			continue
		}
		parsed, parseErr := subtitle.ParseSTPPSample(subtitle.STPPSample{
			DecodeTime: decodeTime, Start: start, End: start + duration,
			Duration: duration, Payload: sample.Payload,
		}, subtitle.TimingParameters{})
		if parseErr != nil {
			n.log.Warn("TTML sample ignored", "track", ts.name, "segment", seg.Number, "err", parseErr)
			continue
		}
		cues = append(cues, parsed...)
	}
	webvtt, err := subtitle.NewWebVTTSegment(cues, windowStart, windowStart+windowDuration,
		mpegTimestamp(decoded.BaseTime, uint64(decoded.Timescale)))
	if err != nil {
		res.err = err
		return res
	}
	payload, err := subtitle.MarshalWebVTT(webvtt)
	if err != nil {
		res.err = err
		return res
	}
	if !reservation.resizeContext(ctx, addBytes(int64(cap(raw)), int64(cap(payload)))) {
		res.err = errors.New("WebVTT segment memory budget exhausted")
		return res
	}
	res.staged, err = n.pub.Stage(payload)
	if err != nil {
		res.err = err
		return res
	}
	res.baseTime = decoded.BaseTime
	res.duration = decoded.Duration
	res.events = decoded.Events
	res.dur = windowDuration.Seconds()
	reservation.release()
	n.observeBudget("staged_text")
	return res
}

func mpegTimestamp(ticks, timescale uint64) uint64 {
	if timescale == 0 {
		return 0
	}
	whole := ticks / timescale
	if whole > math.MaxUint64/90000 {
		return 0
	}
	return (whole*90000 + (ticks%timescale)*90000/timescale) & ((1 << 33) - 1)
}

func (n *Native) run(ctx context.Context, pres *mpd.Presentation) {
	defer close(n.done)

	if !pres.Dynamic {
		if err := n.advance(ctx, pres); err != nil {
			n.fail(err)
			return
		}
		n.pub.Complete()
		<-ctx.Done()
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
			n.log.Warn("mpd refresh failed", "err", safeLogError(err))
			timer.Reset(interval)
			continue
		}
		n.lastManifestOK = n.now()
		advanceErr := n.advance(ctx, next)
		if advanceErr != nil {
			if ctx.Err() != nil {
				return
			}
			var fatal *fatalError
			if errors.As(advanceErr, &fatal) {
				n.fail(advanceErr)
				return
			}
			n.log.Warn("segment publish failed", "err", advanceErr)
		} else if !next.Dynamic {
			n.pub.Complete()
			<-ctx.Done()
			return
		}
		if next.Dynamic {
			if err := n.checkStalled(); err != nil {
				n.fail(err)
				return
			}
		}
		if p := next.MinimumUpdatePeriod; p > 0 {
			interval = p
		}
		timer.Reset(interval)
	}
}

func (n *Native) refreshManifest(ctx context.Context) (*mpd.Presentation, error) {
	if n.forceResolve.Swap(false) && n.refresh != n.opts.ManifestURL {
		n.log.Warn("media stalled on the pinned session, re-resolving from the entry url",
			"pinned", safeLogURL(n.refresh))
		return n.resolveEntry(ctx)
	}

	pres, err := fetchManifest(ctx, n.opts, n.refresh)
	if err == nil {
		n.refresh = pres.Refresh
		return pres, nil
	}
	if ctx.Err() != nil || n.refresh == n.opts.ManifestURL {
		return nil, err
	}
	n.log.Warn("pinned manifest failed, re-resolving from the entry url",
		"pinned", safeLogURL(n.refresh), "err", safeLogError(err))
	return n.resolveEntry(ctx)
}

func safeLogURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted-url>"
	}
	return (&url.URL{Scheme: parsed.Scheme, Host: parsed.Host}).String()
}

func safeLogError(err error) string {
	if err == nil {
		return ""
	}
	return logURLPattern.ReplaceAllStringFunc(err.Error(), safeLogURL)
}

func (n *Native) checkStalled() error {
	if n.opts.StallTimeout <= 0 {
		return nil
	}
	now := n.now()
	if now.Sub(n.lastManifestOK) > n.opts.StallTimeout {
		return nil
	}
	for _, ts := range n.tracks() {
		if now.Sub(ts.lastProgress) <= n.opts.StallTimeout {
			return nil
		}
	}
	return &fatalError{fmt.Errorf("no segment published for %s while the manifest kept updating",
		n.opts.StallTimeout)}
}

func (n *Native) resolveEntry(ctx context.Context) (*mpd.Presentation, error) {
	pres, err := fetchManifest(ctx, n.opts, n.opts.ManifestURL)
	if err != nil {
		return nil, err
	}
	n.reresolves.Add(1)
	n.refresh = pres.Refresh
	return pres, nil
}

type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }
func (e *fatalError) Unwrap() error { return e.err }

func (n *Native) refreshPlan(ctx context.Context, pres *mpd.Presentation) error {
	var replan *Plan
	for _, ts := range n.tracks() {
		if rep, ok := findRepresentation(pres, ts.rep); ok {
			ts.rep = rep
			continue
		}
		if replan == nil {
			plan, err := PlanFromManifestWithSelection(pres, n.opts.PreferHeight, n.opts.Selection)
			if err != nil || !plan.Native() {
				return &fatalError{fmt.Errorf("representation %s is gone and the manifest is no longer usable: %s",
					ts.rep.ID, plan.Reason)}
			}
			replan = &plan
		}
		rep, ok := replacementFor(*replan, ts)
		if !ok {
			if ts.init.Track.Kind == cmaf.KindText {
				n.log.Warn("subtitle representation disappeared from the manifest", "track", ts.name, "representation", ts.rep.ID)
				continue
			}
			return &fatalError{fmt.Errorf("representation %s disappeared from the manifest", ts.rep.ID)}
		}
		n.log.Warn("representation was replaced in the manifest",
			"track", ts.name, "was", ts.rep.ID, "now", rep.ID)
		ts.rep = rep
		if err := n.reanchor(ctx, ts, reasonReplaced); err != nil {
			return err
		}
	}
	return nil
}

func replacementFor(plan Plan, ts *trackState) (mpd.Representation, bool) {
	switch ts.init.Track.Kind {
	case cmaf.KindVideo:
		if ts.name == trackVideo {
			return plan.Video, plan.Video.ID != ""
		}
		for _, video := range plan.Videos {
			if video.Width == ts.rep.Width && video.Height == ts.rep.Height {
				return video, true
			}
		}
	case cmaf.KindAudio:
		for _, audio := range plan.Audios {
			if ts.rep.Lang != "" && audio.Lang == ts.rep.Lang {
				return audio, true
			}
		}
		for _, audio := range plan.Audios {
			if ts.rep.Group != "" && audio.Group == ts.rep.Group {
				return audio, true
			}
		}
		if len(plan.Audios) == 1 {
			return plan.Audios[0], true
		}
	case cmaf.KindText:
		for _, text := range plan.Texts {
			if ts.rep.Lang != "" && text.Lang == ts.rep.Lang {
				return text, true
			}
		}
		if len(plan.Texts) == 1 {
			return plan.Texts[0], true
		}
	}
	return mpd.Representation{}, false
}

func findRepresentation(pres *mpd.Presentation, current mpd.Representation) (mpd.Representation, bool) {
	if len(pres.Periods) == 0 {
		return mpd.Representation{}, false
	}
	for _, rep := range pres.Periods[0].Representations {
		if current.TrackKey != "" && rep.TrackKey == current.TrackKey {
			return rep, true
		}
		if current.TrackKey == "" && rep.ID == current.ID && rep.Group == current.Group {
			return rep, true
		}
	}
	return mpd.Representation{}, false
}

func (n *Native) advance(ctx context.Context, pres *mpd.Presentation) error {
	if err := n.refreshPlan(ctx, pres); err != nil {
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
	for i, ts := range tracks {
		if ts.init.Track.Kind == cmaf.KindText && errs[i] != nil {
			n.log.Warn("subtitle rendition advance failed", "track", ts.name, "err", errs[i])
			errs[i] = nil
		}
	}
	return errors.Join(errs...)
}

func (n *Native) advanceTrack(ctx context.Context, pres *mpd.Presentation, ts *trackState) error {
	segs, err := pres.AvailableSegments(0, ts.rep, n.sourceNow())
	if err != nil {
		return err
	}
	if len(segs) == 0 {
		return nil
	}
	if pres.Dynamic && ts.init.Track.Kind == cmaf.KindText && !ts.hasLast && !ts.forceDiscontinuity {
		pending := n.startWindow(pres, segs)
		if ts != n.video {
			pending = n.holdBehindVideo(ts, pending)
		}
		return n.publishSegments(ctx, ts, pending)
	}

	if pres.Dynamic {
		if reason, ok := n.needsReanchor(ts, segs); ok {

			if reason == reasonNoProgress {
				n.forceResolve.Store(true)
			}
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

func (n *Native) holdBehindVideo(ts *trackState, pending []mpd.Segment) []mpd.Segment {
	watermark := n.video.readyMillis.Load()
	if watermark < 0 || len(pending) == 0 {
		return pending
	}
	hold := n.opts.PrimaryTrackHold.Milliseconds()
	limit := int64(math.MaxInt64)
	if hold <= math.MaxInt64-watermark {
		limit = watermark + hold
	}
	timescale := ts.rep.Addressing.Timescale
	for i, seg := range pending {
		if start, ok := millis(seg.Time, timescale); ok && start > limit {
			n.trackHolds.Add(1)
			n.log.Info("holding rendition behind the primary video watermark", "track", ts.name,
				"video_ready_ms", watermark, "rendition_start_ms", start, "held", len(pending)-i)
			return pending[:i]
		}
	}
	return pending
}

const (
	reasonRollback   = "timeline_rollback"
	reasonNoProgress = "no_progress"
	reasonReplaced   = "representation_replaced"
)

func (n *Native) needsReanchor(ts *trackState, segs []mpd.Segment) (string, bool) {
	if !ts.hasLast {
		return "", false
	}

	newest := segs[len(segs)-1]
	if newest.Time < ts.lastTime {
		return reasonRollback, true
	}

	if n.now().Sub(ts.lastProgress) > n.opts.ReanchorAfter {
		return reasonNoProgress, true
	}
	return "", false
}

func (n *Native) reanchor(ctx context.Context, ts *trackState, reason string) error {
	count := n.reanchors.Add(1)
	n.log.Warn("live re-anchor", "track", ts.name, "reason", reason, "count", count)

	limit := min(n.opts.MaxSegmentBytes, int64(defaultMaxInitBytes))
	init, reservation, err := loadInit(ctx, n.opts, ts.rep, limit)
	if err != nil {
		return err
	}
	defer reservation.release()

	if init.Track.Codec != ts.init.Track.Codec {
		return &fatalError{fmt.Errorf("codec changed from %s to %s on %s; cannot continue natively",
			ts.init.Track.Codec, init.Track.Codec, ts.rep.ID)}
	}
	if init.Track.KID != ts.init.Track.KID {
		if _, ok := n.opts.Keys[init.Track.KID]; !ok {
			return fmt.Errorf("no key for the new kid %s on %s", init.Track.KID, ts.rep.ID)
		}
	}

	initHash := sha256.Sum256(init.Clear)
	if initHash != ts.initHash {
		if err := n.pub.PublishInit(ts.name, init.Clear); err != nil {
			return err
		}
		n.log.Info("init segment changed", "track", ts.name)
	}
	init.Clear = nil
	ts.init = init
	ts.initHash = initHash
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

func (n *Native) Reanchors() uint64 { return n.reanchors.Load() }

func (n *Native) Err() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.err
}

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
