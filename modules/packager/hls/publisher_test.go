package hls

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if err := p.PublishSegment(track, seq, dur, []byte("segment-data"), false); err != nil {
		t.Fatalf("PublishSegment(%s, %d): %v", track, seq, err)
	}
}

// Nothing is playable until every track has an init and a segment; publishing
// a master playlist earlier would point players at a track that cannot start.
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

// A segment that arrives after the frontier has moved past it must be dropped.
// Re-inserting it would reorder a window players may already hold.
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

// A player holding the previous playlist must still be able to fetch a segment
// that just slid out of the window.
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

// TARGETDURATION must round up from the longest segment actually in the window.
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

// Serving is whitelist-based: an asset that was never published is not
// reachable, whatever path a request asks for.
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
	if err := p.PublishSegment("video-main", 1, 2, []byte("segment-data"), false); err != nil {
		t.Fatalf("re-publish: %v", err)
	}
	second, _ := p.Playlist("video-main.m3u8")
	if &first[0] != &second[0] {
		t.Error("playlist snapshot was rewritten even though nothing changed")
	}
}

// A stream whose init changes mid-flight needs a second EXT-X-MAP. Overwriting
// the first one would break every segment already in the window, which still
// decodes against the old map.
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
	// The old map must still be readable by players holding the older window.
	if _, err := os.Stat(filepath.Join(dir, "video-main-init.mp4")); err != nil {
		t.Errorf("the first init segment was overwritten or removed: %v", err)
	}
	if _, ok := p.Asset("video-main-init-2.mp4"); !ok {
		t.Error("the new init segment is not a published asset")
	}
}

// An unchanged init must not churn the playlist with a repeated map.
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

func TestStaticPublicationEndsList(t *testing.T) {
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
	if !strings.Contains(string(pl), "#EXT-X-ENDLIST") {
		t.Errorf("static playlist has no ENDLIST:\n%s", pl)
	}
	if strings.Count(string(pl), ".m4s") != 4 {
		t.Errorf("static playlist must keep every segment:\n%s", pl)
	}
}
