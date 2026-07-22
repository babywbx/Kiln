package hls

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/timedmeta"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestPublisher(t *testing.T, size int, grace time.Duration) (*Publisher, *clock, string) {
	t.Helper()
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	dir := t.TempDir()
	p, err := New(Config{Dir: dir, PlaylistSize: size, Grace: grace, Now: c.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.AddTrack(Track{Name: "video-main", Kind: KindVideo, Codec: "hvc1.1.6.L60.90", Bandwidth: 120000, Width: 320, Height: 180}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := p.AddTrack(Track{Name: "audio-main", Kind: KindAudio, Codec: "mp4a.40.2", Bandwidth: 32000, Channels: 2, Lang: "en"}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := p.PublishInit("video-main", []byte("video-init")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	if err := p.PublishInit("audio-main", []byte("audio-init")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	return p, c, dir
}

func publish(t *testing.T, p *Publisher, track string, seq uint64, dur float64) {
	t.Helper()
	pub := Publication{Track: track, Seq: seq, Duration: dur}
	if err := p.PublishSegment(pub, []byte("segment-data")); err != nil {
		t.Fatalf("PublishSegment(%s, %d): %v", track, seq, err)
	}
}

func TestRefreshesOnlyChangedPlaylists(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	mediaCalls := map[string]int{}
	masterCalls := 0
	p.encoder = playlistEncoder{
		media: func(track *track, static bool) []byte {
			mediaCalls[track.Name]++
			return mediaPlaylist(track, static)
		},
		master: func(tracks []*track, audioGroups bool) []byte {
			masterCalls++
			return masterPlaylist(tracks, audioGroups)
		},
	}

	publish(t, p, "video-main", 1, 2)
	if len(mediaCalls) != 0 || masterCalls != 0 {
		t.Fatalf("encoded before playable: media=%v master=%d", mediaCalls, masterCalls)
	}
	publish(t, p, "audio-main", 1, 2)
	if mediaCalls["video-main"] != 1 || mediaCalls["audio-main"] != 1 || masterCalls != 1 {
		t.Fatalf("initial calls: media=%v master=%d", mediaCalls, masterCalls)
	}

	publish(t, p, "video-main", 2, 2)
	if mediaCalls["video-main"] != 2 {
		t.Errorf("video calls = %d, want 2", mediaCalls["video-main"])
	}
	if mediaCalls["audio-main"] != 1 {
		t.Errorf("audio calls = %d, want 1", mediaCalls["audio-main"])
	}
	if masterCalls != 1 {
		t.Errorf("master calls = %d, want 1", masterCalls)
	}
}

func TestMasterHonorsPreferredDefaultAudio(t *testing.T) {
	got := string(masterPlaylist([]*track{
		{Track: Track{Name: "video", Kind: KindVideo, Codec: "hvc1", Bandwidth: 1000}},
		{Track: Track{Name: "audio-zh", Kind: KindAudio, Codec: "mp4a", Lang: "zho"}},
		{Track: Track{Name: "audio-yue", Kind: KindAudio, Codec: "mp4a", Lang: "yue", Default: true}},
	}, true))
	for _, want := range []string{
		`LANGUAGE="zho",DEFAULT=NO`,
		`LANGUAGE="yue",DEFAULT=YES`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("master missing %q:\n%s", want, got)
		}
	}
}

func TestNotPlayableUntilAllTracksHaveMedia(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	if p.Playable() {
		t.Fatal("playable with no media segments")
	}
	publish(t, p, "video-main", 1, 2)
	if p.Playable() {
		t.Fatal("playable with video only")
	}
	if _, ok := p.Playlist(MasterName); ok {
		t.Fatal("master playlist exists before audio is ready")
	}
	publish(t, p, "audio-main", 1, 2)
	if !p.Playable() {
		t.Fatal("not playable after both tracks have a segment")
	}
	if _, ok := p.Playlist(MasterName); !ok {
		t.Fatal("no master playlist once playable")
	}
}

func TestFrontierIsMonotonic(t *testing.T) {
	p, _, _ := newTestPublisher(t, 6, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	for _, seq := range []uint64{1, 2, 3} {
		publish(t, p, "video-main", seq, 2)
	}
	publish(t, p, "video-main", 2, 2)

	pl, ok := p.Playlist("video-main.m3u8")
	if !ok {
		t.Fatal("no video playlist")
	}
	if got := strings.Count(string(pl), "video-main-000002.m4s"); got != 1 {
		t.Errorf("late segment appears %d times, want 1", got)
	}
	if f := p.Frontier()["video-main"]; f != 3 {
		t.Errorf("frontier = %d, want 3", f)
	}
	if strings.Count(string(pl), ".m4s") != 3 {
		t.Errorf("playlist should still have 3 segments:\n%s", pl)
	}
}

func TestSlidingWindowAdvancesMediaSequence(t *testing.T) {
	p, _, _ := newTestPublisher(t, 3, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	for seq := uint64(1); seq <= 5; seq++ {
		publish(t, p, "video-main", seq, 2)
	}
	pl, _ := p.Playlist("video-main.m3u8")
	if !strings.Contains(string(pl), "#EXT-X-MEDIA-SEQUENCE:2") {
		t.Errorf("media sequence did not advance:\n%s", pl)
	}
	if strings.Contains(string(pl), "video-main-000002.m4s") {
		t.Errorf("expired segment is still listed:\n%s", pl)
	}
	for _, want := range []string{"video-main-000003.m4s", "video-main-000005.m4s"} {
		if !strings.Contains(string(pl), want) {
			t.Errorf("playlist is missing %s:\n%s", want, pl)
		}
	}
}

func TestExpiredSegmentSurvivesGracePeriod(t *testing.T) {
	p, c, dir := newTestPublisher(t, 2, 30*time.Second)
	publish(t, p, "audio-main", 1, 2)
	for seq := uint64(1); seq <= 3; seq++ {
		publish(t, p, "video-main", seq, 2)
	}

	dropped := "video-main-000001.m4s"
	if _, ok := p.Asset(dropped); !ok {
		t.Fatal("segment was deleted the moment it left the playlist")
	}
	if _, err := os.Stat(filepath.Join(dir, dropped)); err != nil {
		t.Fatalf("dropped segment is gone from disk during grace: %v", err)
	}

	c.add(31 * time.Second)
	publish(t, p, "video-main", 4, 2)

	if _, ok := p.Asset(dropped); ok {
		t.Error("segment is still served after the grace period")
	}
	if _, err := os.Stat(filepath.Join(dir, dropped)); !os.IsNotExist(err) {
		t.Errorf("segment was not reaped after the grace period: %v", err)
	}
}

func TestExpiredCleanupDoesNotBlockPublishedState(t *testing.T) {
	p, c, _ := newTestPublisher(t, 1, time.Second)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)
	publish(t, p, "video-main", 2, 2)
	c.add(2 * time.Second)

	const expired = "video-main-000001.m4s"
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseCleanup) })
	p.removeFile = func(path string) error {
		if filepath.Base(path) == expired {
			close(cleanupStarted)
			<-releaseCleanup
		}
		return os.Remove(path)
	}

	staged, err := p.Stage([]byte("segment-data"))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	published := make(chan error, 1)
	go func() {
		published <- p.PublishStaged(Publication{Track: "video-main", Seq: 3, Duration: 2}, staged)
	}()

	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("expired asset cleanup did not start")
	}

	visible := make(chan bool, 1)
	go func() {
		playlist, playlistOK := p.Playlist("video-main.m3u8")
		_, assetOK := p.Asset("video-main-000003.m4s")
		visible <- playlistOK && assetOK && strings.Contains(string(playlist), "video-main-000003.m4s")
	}()
	select {
	case ok := <-visible:
		if !ok {
			t.Fatal("new media was not visible while expired media was being removed")
		}
	case <-time.After(time.Second):
		t.Fatal("published state was blocked by expired asset cleanup")
	}

	releaseOnce.Do(func() { close(releaseCleanup) })
	if err := <-published; err != nil {
		t.Fatalf("PublishStaged: %v", err)
	}
}

func TestStageWriteRemovesPartialFileAfterFailure(t *testing.T) {
	p, _, dir := newTestPublisher(t, 1, time.Second)
	want := errors.New("encode failed")
	staged, err := p.StageWrite(func(dst io.Writer) error {
		if _, writeErr := dst.Write([]byte("partial")); writeErr != nil {
			return writeErr
		}
		return want
	})
	if staged != "" {
		t.Fatalf("staged path = %q after failure", staged)
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("partial stage was not removed: %s", entry.Name())
		}
	}
}

func TestStageWriteRemovesPartialFileAfterPanic(t *testing.T) {
	p, _, dir := newTestPublisher(t, 1, time.Second)
	want := errors.New("encoder panic")
	func() {
		defer func() {
			if recovered := recover(); recovered != want {
				t.Fatalf("recovered = %v, want %v", recovered, want)
			}
		}()
		_, _ = p.StageWrite(func(dst io.Writer) error {
			_, _ = dst.Write([]byte("partial"))
			panic(want)
		})
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("partial stage was not removed: %s", entry.Name())
		}
	}
}

func TestStageWriteRemovesPartialFileAfterCacheFlushFailure(t *testing.T) {
	p, _, dir := newTestPublisher(t, 1, time.Second)
	want := errors.New("sync failed")
	p.dropAfterWrite = func(*os.File) error { return want }

	staged, err := p.Stage([]byte("complete segment"))

	if staged != "" {
		t.Fatalf("staged path = %q after cache flush failure", staged)
	}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".tmp-") {
			t.Fatalf("failed stage was not removed: %s", entry.Name())
		}
	}
}

func TestExpiredCleanupClearsRetainedSliceSlots(t *testing.T) {
	p, c, _ := newTestPublisher(t, 1, time.Second)
	publish(t, p, "audio-main", 1, 2)
	end := c.t.Add(time.Second)
	if err := p.PublishSegment(Publication{
		Track: "video-main", Seq: 1, Duration: 2, At: c.t,
		DateRanges: []timedmeta.DateRange{{
			ID: "ad-break", Class: "com.apple.hls.scte35", StartDate: c.t, EndDate: &end,
		}},
	}, []byte("segment-data")); err != nil {
		t.Fatalf("PublishSegment(video-main, 1): %v", err)
	}
	if err := p.PublishSegment(Publication{
		Track: "video-main", Seq: 2, Duration: 2, At: c.t.Add(2 * time.Second),
	}, []byte("segment-data")); err != nil {
		t.Fatalf("PublishSegment(video-main, 2): %v", err)
	}

	c.add(2 * time.Second)
	if err := p.PublishInit("video-main", []byte("video-init-2")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}

	p.mu.RLock()
	defer p.mu.RUnlock()
	video := p.tracks["video-main"]
	if len(video.tombstones) != 0 {
		t.Fatalf("expired tombstones = %d, want 0", len(video.tombstones))
	}
	retained := video.tombstones[:cap(video.tombstones)]
	for index, tombstone := range retained {
		if tombstone.Name != "" || tombstone.Parts != nil || tombstone.DateRanges != nil {
			t.Fatalf("retained tombstone slot %d still references expired media", index)
		}
	}
}

func TestTargetDurationRoundsUp(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 1.92)
	publish(t, p, "video-main", 2, 2.08)

	pl, _ := p.Playlist("video-main.m3u8")
	if !strings.Contains(string(pl), "#EXT-X-TARGETDURATION:3") {
		t.Errorf("target duration should round 2.08 up to 3:\n%s", pl)
	}
}

func TestTargetDurationIsPinnedToTheDeclaredMaximum(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p, err := New(Config{
		Dir:                t.TempDir(),
		PlaylistSize:       4,
		Grace:              time.Minute,
		MaxSegmentDuration: 12 * time.Second,
		Now:                c.now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.AddTrack(Track{Name: "video-main", Kind: KindVideo, Codec: "hvc1.1.6.L60.90"}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := p.PublishInit("video-main", []byte("video-init")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}

	for seq := uint64(1); seq <= 3; seq++ {
		publish(t, p, "video-main", seq, 8)
		pl, _ := p.Playlist("video-main.m3u8")
		if !strings.Contains(string(pl), "#EXT-X-TARGETDURATION:12") {
			t.Fatalf("target duration must be the declared 12 from the start:\n%s", pl)
		}
	}
	publish(t, p, "video-main", 4, 11.5)
	pl, _ := p.Playlist("video-main.m3u8")
	if !strings.Contains(string(pl), "#EXT-X-TARGETDURATION:12") {
		t.Errorf("a segment within the declared maximum must not move the target:\n%s", pl)
	}
}

func TestUnpublishedAssetIsNotServed(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)

	for _, name := range []string{"../../etc/passwd", "video-main-000099.m4s", "master.m3u8"} {
		if _, ok := p.Asset(name); ok {
			t.Errorf("%s should not resolve to an asset", name)
		}
	}
	if _, ok := p.Asset("video-main-000001.m4s"); !ok {
		t.Error("a published segment should resolve")
	}
}

func TestPlaylistSnapshotIsStableWhenNothingChanges(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)

	first, _ := p.Playlist("video-main.m3u8")
	repeat := Publication{Track: "video-main", Seq: 1, Duration: 2}
	if err := p.PublishSegment(repeat, []byte("segment-data")); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	second, _ := p.Playlist("video-main.m3u8")
	if &first[0] != &second[0] {
		t.Error("playlist snapshot was rewritten even though nothing changed")
	}
}

func TestInitChangeEmitsANewMap(t *testing.T) {
	p, _, dir := newTestPublisher(t, 6, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)

	if err := p.PublishInit("video-main", []byte("video-init-v2")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	publish(t, p, "video-main", 2, 2)

	pl, _ := p.Playlist("video-main.m3u8")
	if got := strings.Count(string(pl), "#EXT-X-MAP:"); got != 2 {
		t.Fatalf("playlist has %d maps, want 2:\n%s", got, pl)
	}
	for _, want := range []string{
		`#EXT-X-MAP:URI="video-main-init.mp4"`,
		`#EXT-X-MAP:URI="video-main-init-2.mp4"`,
	} {
		if !strings.Contains(string(pl), want) {
			t.Errorf("playlist is missing %s:\n%s", want, pl)
		}
	}

	if _, err := os.Stat(filepath.Join(dir, "video-main-init.mp4")); err != nil {
		t.Errorf("the first init segment was overwritten or removed: %v", err)
	}
	if _, ok := p.Asset("video-main-init-2.mp4"); !ok {
		t.Error("the new init segment is not a published asset")
	}
}

func TestUnchangedInitDoesNotRepeatTheMap(t *testing.T) {
	p, _, _ := newTestPublisher(t, 6, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)
	publish(t, p, "video-main", 2, 2)

	pl, _ := p.Playlist("video-main.m3u8")
	if got := strings.Count(string(pl), "#EXT-X-MAP:"); got != 1 {
		t.Errorf("playlist has %d maps, want 1:\n%s", got, pl)
	}
}

func TestInitAssetsFollowDynamicReachability(t *testing.T) {
	p, c, dir := newTestPublisher(t, 2, 30*time.Second)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)
	if err := p.PublishInit("video-main", []byte("init-2")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	publish(t, p, "video-main", 2, 2)
	if err := p.PublishInit("video-main", []byte("init-3")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	publish(t, p, "video-main", 3, 2)

	if _, ok := p.Asset("video-main-init.mp4"); !ok {
		t.Fatal("tombstone init was reaped during grace")
	}
	if _, ok := p.Asset("video-main-init-2.mp4"); !ok {
		t.Fatal("active init was reaped")
	}
	if _, ok := p.Asset("video-main-init-3.mp4"); !ok {
		t.Fatal("current init was reaped")
	}

	c.add(31 * time.Second)
	publish(t, p, "video-main", 4, 2)
	if _, ok := p.Asset("video-main-init.mp4"); ok {
		t.Error("unreachable init still resolves")
	}
	if _, err := os.Stat(filepath.Join(dir, "video-main-init.mp4")); !os.IsNotExist(err) {
		t.Errorf("unreachable init remains on disk: %v", err)
	}
}

func TestRepeatedInitChangesRemainBounded(t *testing.T) {
	p, c, _ := newTestPublisher(t, 2, time.Second)
	publish(t, p, "audio-main", 1, 2)
	for seq := uint64(1); seq <= 12; seq++ {
		if err := p.PublishInit("video-main", []byte{byte(seq)}); err != nil {
			t.Fatalf("PublishInit: %v", err)
		}
		publish(t, p, "video-main", seq, 2)
		c.add(2 * time.Second)
	}
	count := 0
	for name := range p.assets {
		if strings.HasPrefix(name, "video-main-init") {
			count++
		}
	}
	if count > 3 {
		t.Errorf("dynamic init assets grew to %d, want at most 3", count)
	}
}

func TestUnreferencedInitChangesAreReapedWithoutMediaProgress(t *testing.T) {
	p, _, _ := newTestPublisher(t, 2, time.Second)
	for version := byte(1); version <= 12; version++ {
		if err := p.PublishInit("video-main", []byte{version}); err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for name := range p.assets {
		if strings.HasPrefix(name, "video-main-init") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("unreferenced init assets = %d, want 1", count)
	}
}

func TestInitRemovalFailureDoesNotBreakPublication(t *testing.T) {
	p, c, dir := newTestPublisher(t, 1, time.Second)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)
	oldInit := filepath.Join(dir, "video-main-init.mp4")
	if err := os.Remove(oldInit); err != nil {
		t.Fatalf("remove init: %v", err)
	}
	if err := os.Mkdir(oldInit, 0o750); err != nil {
		t.Fatalf("replace init with directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldInit, "child"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write child: %v", err)
	}
	if err := p.PublishInit("video-main", []byte("init-2")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	publish(t, p, "video-main", 2, 2)
	c.add(2 * time.Second)
	publish(t, p, "video-main", 3, 2)
	if _, ok := p.Asset("video-main-init.mp4"); ok {
		t.Fatal("failed removal left init resolvable")
	}
	if _, err := os.Stat(oldInit); err != nil {
		t.Fatalf("failed removal did not leave init on disk: %v", err)
	}
	if err := os.Remove(filepath.Join(oldInit, "child")); err != nil {
		t.Fatalf("remove child: %v", err)
	}
	if err := os.Remove(oldInit); err != nil {
		t.Fatalf("restore removable init: %v", err)
	}
	if err := os.WriteFile(oldInit, []byte("old-init"), 0o600); err != nil {
		t.Fatalf("restore init file: %v", err)
	}
	publish(t, p, "video-main", 4, 2)
	if _, ok := p.Asset("video-main-init.mp4"); ok {
		t.Error("retried removal made init resolvable")
	}
	if _, err := os.Stat(oldInit); !os.IsNotExist(err) {
		t.Errorf("retried removal left init on disk: %v", err)
	}
	if _, ok := p.Asset("video-main-000004.m4s"); !ok {
		t.Error("publication stopped after init removal failed")
	}
}

func TestStaticPublicationKeepsEveryInitAsset(t *testing.T) {
	p, err := New(Config{Dir: t.TempDir(), PlaylistSize: 2, Static: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.AddTrack(Track{Name: "video-main", Kind: KindVideo}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	for seq := uint64(1); seq <= 12; seq++ {
		if err := p.PublishInit("video-main", []byte{byte(seq)}); err != nil {
			t.Fatalf("PublishInit: %v", err)
		}
		publish(t, p, "video-main", seq, 2)
	}
	for n := 1; n <= 12; n++ {
		name := "video-main-init.mp4"
		if n > 1 {
			name = fmt.Sprintf("video-main-init-%d.mp4", n)
		}
		if _, ok := p.Asset(name); !ok {
			t.Errorf("static init %s was removed", name)
		}
	}
}

func TestStaticPublicationEndsOnlyAfterCompletion(t *testing.T) {
	c := &clock{t: time.Now()}
	p, err := New(Config{Dir: t.TempDir(), PlaylistSize: 2, Static: true, Now: c.now})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.AddTrack(Track{Name: "video-main", Kind: KindVideo, Codec: "avc1.42C00C"}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := p.PublishInit("video-main", []byte("init")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	for seq := uint64(1); seq <= 4; seq++ {
		publish(t, p, "video-main", seq, 2)
	}
	pl, _ := p.Playlist("video-main.m3u8")
	if strings.Contains(string(pl), "#EXT-X-ENDLIST") {
		t.Errorf("incomplete static playlist ended early:\n%s", pl)
	}

	p.Complete()
	pl, _ = p.Playlist("video-main.m3u8")
	if !strings.Contains(string(pl), "#EXT-X-ENDLIST") {
		t.Errorf("completed static playlist has no ENDLIST:\n%s", pl)
	}
	if strings.Count(string(pl), ".m4s") != 4 {
		t.Errorf("static playlist must keep every segment:\n%s", pl)
	}
}

func TestStaticPublicationAppendsWithoutReencodingHistory(t *testing.T) {
	p, err := New(Config{Dir: t.TempDir(), Static: true, MaxSegmentDuration: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AddTrack(Track{Name: "video-main", Kind: KindVideo, Codec: "avc1.42C00C"}); err != nil {
		t.Fatal(err)
	}
	if err := p.PublishInit("video-main", []byte("init")); err != nil {
		t.Fatal(err)
	}
	mediaCalls := 0
	p.encoder.media = func(track *track, endList bool) []byte {
		mediaCalls++
		return mediaPlaylist(track, endList)
	}

	publish(t, p, "video-main", 1, 2)
	first, _ := p.Playlist("video-main.m3u8")
	wantFirst := string(first)
	for seq := uint64(2); seq <= 100; seq++ {
		publish(t, p, "video-main", seq, 2)
	}
	p.Complete()

	if mediaCalls != 1 {
		t.Fatalf("full media encodes = %d, want 1", mediaCalls)
	}
	if string(first) != wantFirst {
		t.Fatal("an earlier playlist snapshot changed after append")
	}
	final, _ := p.Playlist("video-main.m3u8")
	if got := strings.Count(string(final), ".m4s"); got != 100 {
		t.Fatalf("segments = %d, want 100", got)
	}
	if !strings.Contains(string(final), "#EXT-X-ENDLIST") {
		t.Fatal("completed playlist has no ENDLIST")
	}
}

func TestStaticDateRangeDoesNotDisableIncrementalAppend(t *testing.T) {
	t.Parallel()

	p, err := New(Config{Dir: t.TempDir(), Static: true, MaxSegmentDuration: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.AddTrack(Track{Name: "video-main", Kind: KindVideo, Codec: "avc1.42C00C"}); err != nil {
		t.Fatal(err)
	}
	if err := p.PublishInit("video-main", []byte("init")); err != nil {
		t.Fatal(err)
	}
	mediaCalls := 0
	p.encoder.media = func(track *track, endList bool) []byte {
		mediaCalls++
		return mediaPlaylist(track, endList)
	}
	start := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	if err := p.PublishSegment(Publication{
		Track: "video-main", Seq: 1, Duration: 2, At: start,
		DateRanges: []timedmeta.DateRange{{
			ID: "scte35-7", Class: "com.apple.hls.scte35", StartDate: start, SCTE35Out: "0xFC01",
		}},
	}, []byte("segment")); err != nil {
		t.Fatal(err)
	}
	for sequence := uint64(2); sequence <= 100; sequence++ {
		if err := p.PublishSegment(Publication{
			Track: "video-main", Seq: sequence, Duration: 2,
			At: start.Add(time.Duration(sequence-1) * 2 * time.Second),
		}, []byte("segment")); err != nil {
			t.Fatal(err)
		}
	}
	if mediaCalls != 1 {
		t.Fatalf("full media encodes = %d, want 1", mediaCalls)
	}
}
