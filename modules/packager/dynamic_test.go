package packager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

type liveOrigin struct {
	t *testing.T

	mu sync.Mutex

	timelineStart uint64

	videoCount int
	audioCount int

	videoChunks []string
	audioChunks []string
	videoInit   string
	audioInit   string

	audios []audioSet

	prefix string

	videoID string
}

type audioSet struct {
	prefix string
	lang   string
	reps   []audioRep
}

type audioRep struct {
	id        string
	bandwidth int
	codecs    string
}

const (
	liveTimescale = 1000
	liveSegTicks  = 2000
)

func newLiveOrigin(t *testing.T) *liveOrigin {
	return &liveOrigin{
		t: t,

		timelineStart: 6000,
		videoCount:    3,
		audioCount:    3,
		videoInit:     "init-stream0.m4s",
		audioInit:     "init-stream1.m4s",
		videoChunks:   []string{"chunk-stream0-00001.m4s", "chunk-stream0-00002.m4s", "chunk-stream0-00003.m4s"},
		audioChunks:   []string{"chunk-stream1-00001.m4s", "chunk-stream1-00002.m4s", "chunk-stream1-00002.m4s"},
		audios:        []audioSet{{prefix: "a", reps: []audioRep{{id: "a0", bandwidth: 32000}}}},
		videoID:       "v0",
	}
}

func (o *liveOrigin) rename(video, audio string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.videoID = video
	o.audios[0].reps[0].id = audio
}

func (o *liveOrigin) manifest() []byte {
	o.mu.Lock()
	start := o.timelineStart
	o.mu.Unlock()
	return o.manifestFrom(start)
}

func (o *liveOrigin) manifestFrom(start uint64) []byte {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := fmt.Appendf(nil, `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic"
     availabilityStartTime="2026-01-01T00:00:00Z"
     minimumUpdatePeriod="PT1S" timeShiftBufferDepth="PT60S">
  <Period id="0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4" codecs="hvc1.1.6.L60.90">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
      <SegmentTemplate timescale="%d" initialization="%sv/init.m4s" media="%sv/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="%d" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="%s" bandwidth="120000" width="320" height="180"/>
    </AdaptationSet>`,
		liveTimescale, o.prefix, o.prefix, start, liveSegTicks, o.videoCount-1, o.videoID)

	for i, set := range o.audios {
		out = fmt.Appendf(out, `
    <AdaptationSet id="%d" contentType="audio" mimeType="audio/mp4" codecs="mp4a.40.2" lang="%s">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
      <SegmentTemplate timescale="%d" initialization="%s%s/init.m4s" media="%s%s/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="%d" d="%d" r="%d"/></SegmentTimeline>
      </SegmentTemplate>`,
			i+1, set.lang, liveTimescale, o.prefix, set.prefix, o.prefix, set.prefix, start, liveSegTicks, o.audioCount-1)
		for _, rep := range set.reps {
			if rep.codecs != "" {
				out = fmt.Appendf(out, "\n      <Representation id=%q bandwidth=\"%d\" codecs=%q/>", rep.id, rep.bandwidth, rep.codecs)
				continue
			}
			out = fmt.Appendf(out, "\n      <Representation id=%q bandwidth=\"%d\"/>", rep.id, rep.bandwidth)
		}
		out = append(out, "\n    </AdaptationSet>"...)
	}
	return append(out, "\n  </Period>\n</MPD>"...)
}

func (o *liveOrigin) grow(segments int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.videoCount += segments
	o.audioCount += segments
}

func (o *liveOrigin) stallVideo(extraAudio int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.audioCount += extraAudio
}

func (o *liveOrigin) Fetch(_ context.Context, url string) ([]byte, string, error) {
	if strings.Contains(url, "stream.mpd") {
		return o.manifest(), url, nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()

	base := url[strings.LastIndex(url, "/live/")+len("/live/"):]
	if o.prefix != "" {
		base = strings.TrimPrefix(base, o.prefix)
	}
	kind, name, ok := strings.Cut(base, "/")
	if !ok {
		return nil, "", fmt.Errorf("unexpected url %s", url)
	}
	video := kind == "v"
	if name == "init.m4s" {
		if video {
			return o.read(o.videoInit), url, nil
		}
		return o.read(o.audioInit), url, nil
	}

	var index int
	if _, err := fmt.Sscanf(name, "seg-%d.m4s", &index); err != nil {
		return nil, "", fmt.Errorf("unexpected url %s", url)
	}

	chunks := o.audioChunks
	if video {
		chunks = o.videoChunks
	}
	return o.read(chunks[(index-1)%len(chunks)]), url, nil
}

func (o *liveOrigin) read(name string) []byte {
	o.t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cenc", "hevc", name))
	if err != nil {
		o.t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

func (o *liveOrigin) rollback() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.timelineStart = 0
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func startLive(t *testing.T, origin *liveOrigin, clock *fakeClock) (*Native, string) {
	t.Helper()
	dir := t.TempDir()
	n, err := StartNative(context.Background(), Options{
		ManifestURL:   "https://origin.example.com/live/stream.mpd",
		Dir:           dir,
		Keys:          keys(t),
		Fetcher:       origin,
		StartSegments: 3,
		PlaylistSize:  10,
		ReanchorAfter: 30 * time.Second,
		Now:           clock.now,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = n.Stop() })
	return n, dir
}

func newClock() *fakeClock {

	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 12, 0, time.UTC)}
}

func videoPlaylist(t *testing.T, n *Native) string {
	t.Helper()
	pl, ok := n.Publication().Playlist("video-main.m3u8")
	if !ok {
		t.Fatal("no video playlist")
	}
	return string(pl)
}

func parseManifest(t *testing.T, origin *liveOrigin) *mpd.Presentation {
	t.Helper()
	pres, err := mpd.Parse(origin.manifest(), "https://origin.example.com/live/stream.mpd")
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return pres
}

func TestDynamicSourcePublishesContiguousSegments(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	if n.PackMode() != "dynamic_timeline" {
		t.Errorf("pack mode = %s, want dynamic_timeline", n.PackMode())
	}
	pl := videoPlaylist(t, n)
	if got := strings.Count(pl, ".m4s"); got != 3 {
		t.Fatalf("playlist has %d segments, want 3:\n%s", got, pl)
	}

	if strings.Contains(pl, "#EXT-X-DISCONTINUITY\n") {
		t.Errorf("contiguous segments were marked as a discontinuity:\n%s", pl)
	}
	if !strings.Contains(pl, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Errorf("media sequence should start at 0:\n%s", pl)
	}
}

func TestRolloverReanchorsInsteadOfStalling(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	before := strings.Count(videoPlaylist(t, n), ".m4s")
	if n.Reanchors() != 0 {
		t.Fatalf("re-anchored during a normal start")
	}

	origin.rollback()
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance after rollover: %v", err)
	}

	if n.Reanchors() == 0 {
		t.Fatal("a timeline rollback did not trigger a re-anchor")
	}
	pl := videoPlaylist(t, n)
	if got := strings.Count(pl, ".m4s"); got <= before {
		t.Fatalf("no segment was published after the rollover (%d segments):\n%s", got, pl)
	}
	if !strings.Contains(pl, "#EXT-X-DISCONTINUITY") {
		t.Errorf("a re-anchor must be signalled as a discontinuity:\n%s", pl)
	}
	assertSequenceIncreases(t, pl)
}

func TestNoProgressTriggersReanchor(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	clock.advance(31 * time.Second)
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if n.Reanchors() == 0 {
		t.Fatal("a stalled publication did not re-anchor")
	}
	assertSequenceIncreases(t, videoPlaylist(t, n))
}

func TestAudioIsHeldBehindAStalledVideo(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	origin.stallVideo(20)
	clock.advance(20 * time.Second)
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance: %v", err)
	}

	if n.Stats().TrackHolds == 0 {
		t.Fatal("audio was never held behind the stalled video")
	}
	if n.Reanchors() != 0 {
		t.Fatal("the hold must not be a re-anchor in disguise")
	}
	video := n.video.readyMillis.Load()
	audio := n.audios[0].readyMillis.Load()
	limit := video + defaultPrimaryTrackHold.Milliseconds() + liveSegTicks
	if audio > limit {
		t.Fatalf("audio ran to %d ms while video is stuck at %d ms (limit %d)", audio, video, limit)
	}
	if audio <= video {
		t.Fatalf("audio did not advance at all (%d ms); the hold is too tight", audio)
	}

	clock.advance(15 * time.Second)
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance after the hold expired: %v", err)
	}
	if n.Reanchors() == 0 {
		t.Fatal("a video stalled past ReanchorAfter never re-anchored; the hold blocks forever")
	}
}

const entryURL = "https://origin.example.com/live/stream.mpd"

type rotatingOrigin struct {
	*liveOrigin

	rmu      sync.Mutex
	sessions int
	hits     map[string]int
	dead     bool
}

func newRotatingOrigin(t *testing.T) *rotatingOrigin {
	return &rotatingOrigin{liveOrigin: newLiveOrigin(t), hits: map[string]int{}}
}

func (o *rotatingOrigin) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	if !strings.Contains(url, "stream.mpd") {
		return o.liveOrigin.Fetch(ctx, url)
	}

	o.rmu.Lock()
	o.hits[url]++
	pinned := strings.Contains(url, "session=")
	if pinned && o.dead {
		o.rmu.Unlock()
		return nil, "", fmt.Errorf("session expired")
	}
	if !pinned {
		o.sessions++
	}
	session := o.sessions
	o.rmu.Unlock()

	start := o.timelineStart
	if session > 1 {
		start -= liveSegTicks
	}
	return o.manifestFrom(start), fmt.Sprintf("%s?session=%d", entryURL, session), nil
}

func (o *rotatingOrigin) hitsOn(url string) int {
	o.rmu.Lock()
	defer o.rmu.Unlock()
	return o.hits[url]
}

func (o *rotatingOrigin) expireSessions() {
	o.rmu.Lock()
	o.dead = true
	o.rmu.Unlock()
}

func startRotating(t *testing.T, origin *rotatingOrigin, clock *fakeClock) *Native {
	t.Helper()
	n, err := StartNative(context.Background(), Options{
		ManifestURL:   entryURL,
		Dir:           t.TempDir(),
		Keys:          keys(t),
		Fetcher:       origin,
		StartSegments: 3,
		PlaylistSize:  10,
		ReanchorAfter: 30 * time.Second,
		Now:           clock.now,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = n.Stop() })
	return n
}

func TestManifestRefreshStaysOnTheResolvedSession(t *testing.T) {
	origin := newRotatingOrigin(t)
	n := startRotating(t, origin, newClock())

	for i := 0; i < 3; i++ {
		pres, err := n.refreshManifest(context.Background())
		if err != nil {
			t.Fatalf("refresh %d: %v", i, err)
		}
		if err := n.advance(context.Background(), pres); err != nil {
			t.Fatalf("advance %d: %v", i, err)
		}
	}

	if got := origin.hitsOn(entryURL); got != 1 {
		t.Errorf("the entry url was resolved %d times, want 1: every refresh landed on a new edge", got)
	}
	if got := origin.hitsOn(entryURL + "?session=1"); got != 3 {
		t.Errorf("the pinned session was refreshed %d times, want 3", got)
	}
	if n.Reanchors() != 0 {
		t.Errorf("hopping between edges was mistaken for a rollover (%d re-anchors)", n.Reanchors())
	}
}

func TestExpiredSessionReresolvesTheEntryURL(t *testing.T) {
	origin := newRotatingOrigin(t)
	n := startRotating(t, origin, newClock())

	origin.expireSessions()
	pres, err := n.refreshManifest(context.Background())
	if err != nil {
		t.Fatalf("a dead session must be recovered by re-resolving the entry url: %v", err)
	}
	if got := origin.hitsOn(entryURL); got != 2 {
		t.Errorf("the entry url was resolved %d times, want 2", got)
	}
	if pres.Refresh == entryURL+"?session=1" {
		t.Error("the publication is still pinned to the dead session")
	}
}

func TestStatsReflectPublishedWork(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	st := n.Stats()
	if st.SegmentsPublished != 6 {
		t.Errorf("segments published = %d, want 6 (3 video + 3 audio)", st.SegmentsPublished)
	}
	if st.SegmentsFetched != 6 {
		t.Errorf("segments fetched = %d, want 6", st.SegmentsFetched)
	}
	if st.DecryptSeconds < 0 {
		t.Errorf("decrypt time = %.3f, want a non-negative measurement", st.DecryptSeconds)
	}
	if st.CacheBytes <= 0 || st.CacheItems == 0 {
		t.Errorf("cache usage = %d bytes in %d items, want both non-zero", st.CacheBytes, st.CacheItems)
	}
	if st.VideoFrontier != 3 || st.AudioFrontier != 3 {
		t.Errorf("frontier = video %d / audio %d, want 3 / 3", st.VideoFrontier, st.AudioFrontier)
	}

	origin.rollback()
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	after := n.Stats()
	if after.Reanchors == 0 {
		t.Error("a re-anchor was not counted")
	}
	if after.Discontinuities == 0 {
		t.Error("a discontinuity was not counted")
	}
	if after.VideoFrontier <= st.VideoFrontier {
		t.Error("the frontier did not advance after the re-anchor")
	}
}

func assertSequenceIncreases(t *testing.T, playlist string) {
	t.Helper()
	var last string
	for line := range strings.SplitSeq(playlist, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasSuffix(line, ".m4s") {
			continue
		}
		if last != "" && line <= last {
			t.Errorf("published sequence went backwards: %s after %s", line, last)
		}
		last = line
	}
	if last == "" {
		t.Errorf("playlist has no segments:\n%s", playlist)
	}
}

func TestPlaylistNeverReferencesAMissingAsset(t *testing.T) {
	origin := newLiveOrigin(t)
	clock := newClock()
	n, dir := startLive(t, origin, clock)

	origin.rollback()
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance: %v", err)
	}

	for _, name := range []string{"video-main.m3u8", "audio-main.m3u8", hls.MasterName} {
		pl, ok := n.Publication().Playlist(name)
		if !ok {
			t.Fatalf("missing playlist %s", name)
		}
		for line := range strings.SplitSeq(string(pl), "\n") {
			line = strings.TrimSpace(line)
			ref := line
			if strings.HasPrefix(line, "#EXT-X-MAP:URI=") {
				ref = strings.Trim(strings.TrimPrefix(line, "#EXT-X-MAP:URI="), `"`)
			} else if strings.HasPrefix(line, "#") || line == "" {
				continue
			}
			if strings.HasSuffix(ref, ".m3u8") {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, ref)); err != nil {
				t.Errorf("%s references %s, which is not on disk: %v", name, ref, err)
			}
		}
	}
}
