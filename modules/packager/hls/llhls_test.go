package hls

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newLLPublisher(t *testing.T, size int) *Publisher {
	t.Helper()
	p, err := New(Config{
		Dir:          t.TempDir(),
		PlaylistSize: size,
		Grace:        time.Minute,
		LLHLS:        true,
		PartTarget:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.AddTrack(Track{Name: "video", Kind: KindVideo, Codec: "hvc1", Bandwidth: 1}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := p.PublishInit("video", []byte("init")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	return p
}

func publishLLSegment(t *testing.T, p *Publisher, seq uint64) {
	t.Helper()
	if err := p.PublishSegment(Publication{Track: "video", Seq: seq, Duration: 2}, []byte("segment")); err != nil {
		t.Fatalf("PublishSegment(%d): %v", seq, err)
	}
}

func publishPart(t *testing.T, p *Publisher, msn, index uint64, independent bool) {
	t.Helper()
	err := p.PublishPart(PartPublication{
		Track:       "video",
		MSN:         msn,
		Part:        index,
		Duration:    0.4,
		Independent: independent,
	}, []byte("part"))
	if err != nil {
		t.Fatalf("PublishPart(%d, %d): %v", msn, index, err)
	}
}

func TestWholeSegmentsDoNotAdvertisePartialLatency(t *testing.T) {
	p := newLLPublisher(t, 8)
	publishLLSegment(t, p, 1)

	pl, _ := p.Playlist("video.m3u8")
	for _, absent := range []string{"#EXT-X-SERVER-CONTROL", "#EXT-X-PART-INF", "#EXT-X-PART:", "#EXT-X-PRELOAD-HINT"} {
		if strings.Contains(string(pl), absent) {
			t.Fatalf("whole-segment input must not advertise %s:\n%s", absent, pl)
		}
	}
}

func TestPublishPartAdvertisesLowLatencyAndSeparateAsset(t *testing.T) {
	p := newLLPublisher(t, 8)
	publishLLSegment(t, p, 1)
	publishPart(t, p, 1, 0, true)

	pl, _ := p.Playlist("video.m3u8")
	text := string(pl)
	for _, want := range []string{
		"#EXT-X-VERSION:9",
		"#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,CAN-SKIP-UNTIL=12.000000,PART-HOLD-BACK=1.000000",
		"#EXT-X-PART-INF:PART-TARGET=0.500000",
		`#EXT-X-PART:DURATION=0.400000,URI="video-part-000001-000.m4s",INDEPENDENT=YES`,
		`#EXT-X-PRELOAD-HINT:TYPE=PART,URI="video-part-000001-001.m4s"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("playlist missing %q:\n%s", want, text)
		}
	}
	if _, ok := p.Asset("video-part-000001-000.m4s"); !ok {
		t.Fatal("published part is not independently addressable")
	}
	if _, ok := p.Asset("video-part-000001-001.m4s"); ok {
		t.Fatal("preload hint must not masquerade as a completed asset")
	}
}

func TestAssetContextWaitsForPreloadedPart(t *testing.T) {
	p := newLLPublisher(t, 8)
	publishLLSegment(t, p, 1)
	publishPart(t, p, 1, 0, true)

	type result struct {
		path  string
		found bool
		err   error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		path, found, err := p.AssetContext(ctx, "video-part-000001-001.m4s")
		done <- result{path: path, found: found, err: err}
	}()

	publishPart(t, p, 1, 1, false)
	got := <-done
	if got.err != nil || !got.found || !strings.HasSuffix(got.path, "video-part-000001-001.m4s") {
		t.Fatalf("AssetContext = (%q, %v, %v)", got.path, got.found, got.err)
	}
}

func TestCompletedParentKeepsPartsAndAdvancesHint(t *testing.T) {
	p := newLLPublisher(t, 8)
	publishLLSegment(t, p, 1)
	publishPart(t, p, 1, 0, true)
	publishPart(t, p, 1, 1, false)
	publishLLSegment(t, p, 2)

	pl, _ := p.Playlist("video.m3u8")
	text := string(pl)
	part := strings.Index(text, `URI="video-part-000001-000.m4s"`)
	parent := strings.Index(text, "video-000002.m4s")
	if part < 0 || parent < 0 || part > parent {
		t.Fatalf("parts must precede their parent segment:\n%s", text)
	}
	if !strings.Contains(text, `#EXT-X-PRELOAD-HINT:TYPE=PART,URI="video-part-000002-000.m4s"`) {
		t.Fatalf("hint did not advance to the next parent:\n%s", text)
	}
}

func TestDeltaPlaylistSkipsOldSegments(t *testing.T) {
	p := newLLPublisher(t, 8)
	for seq := uint64(1); seq <= 8; seq++ {
		publishLLSegment(t, p, seq)
	}
	publishPart(t, p, 8, 0, true)

	view, ok := p.PlaylistWithOptions("video.m3u8", PlaylistOptions{Skip: true})
	if !ok {
		t.Fatal("no delta playlist")
	}
	text := string(view.Body)
	if !strings.Contains(text, "#EXT-X-SKIP:SKIPPED-SEGMENTS=2") {
		t.Fatalf("delta playlist did not skip the eligible prefix:\n%s", text)
	}
	if strings.Contains(text, "video-000001.m4s") || strings.Contains(text, "video-000002.m4s") {
		t.Fatalf("delta playlist still contains skipped segments:\n%s", text)
	}
	if !strings.Contains(text, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Fatalf("delta playlist changed its media sequence:\n%s", text)
	}
}

func TestPlaylistContextWakesOnRevisionAndHonorsCancellation(t *testing.T) {
	p := newLLPublisher(t, 8)
	publishLLSegment(t, p, 1)
	initial, ok := p.PlaylistWithOptions("video.m3u8", PlaylistOptions{})
	if !ok {
		t.Fatal("no initial playlist")
	}

	type result struct {
		view  PlaylistView
		found bool
		err   error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		view, found, err := p.PlaylistContext(ctx, "video.m3u8", PlaylistRequest{AfterRevision: initial.Revision})
		done <- result{view: view, found: found, err: err}
	}()

	publishPart(t, p, 1, 0, true)
	got := <-done
	if got.err != nil || !got.found || got.view.Revision <= initial.Revision {
		t.Fatalf("PlaylistContext = (rev %d, %v, %v), initial rev %d", got.view.Revision, got.found, got.err, initial.Revision)
	}

	timeout, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	_, _, err := p.PlaylistContext(timeout, "video.m3u8", PlaylistRequest{AfterRevision: got.view.Revision})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestPlaylistContextWaitsForPartAndReturnsOldMSNImmediately(t *testing.T) {
	p := newLLPublisher(t, 2)
	for seq := uint64(1); seq <= 4; seq++ {
		publishLLSegment(t, p, seq)
	}
	publishPart(t, p, 4, 0, true)

	oldMSN := uint64(0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, found, err := p.PlaylistContext(ctx, "video.m3u8", PlaylistRequest{MSN: &oldMSN}); err != nil || !found {
		t.Fatalf("old MSN should return immediately: found=%v err=%v", found, err)
	}

	msn, part := uint64(4), uint64(1)
	done := make(chan error, 1)
	go func() {
		view, found, err := p.PlaylistContext(ctx, "video.m3u8", PlaylistRequest{MSN: &msn, Part: &part})
		if err == nil && (!found || !strings.Contains(string(view.Body), "video-part-000004-001.m4s")) {
			err = errors.New("woke without requested part")
		}
		done <- err
	}()
	publishPart(t, p, 4, 1, false)
	if err := <-done; err != nil {
		t.Fatalf("part wait: %v", err)
	}

	ahead := uint64(20)
	_, _, err := p.PlaylistContext(ctx, "video.m3u8", PlaylistRequest{MSN: &ahead})
	if !errors.Is(err, ErrPlaylistRequestAhead) {
		t.Fatalf("far-ahead request error = %v", err)
	}
}

func TestOldMSNForcesFullPlaylistEvenWhenSkipWasRequested(t *testing.T) {
	p := newLLPublisher(t, 8)
	for seq := uint64(1); seq <= 10; seq++ {
		publishLLSegment(t, p, seq)
	}
	publishPart(t, p, 10, 0, true)

	old := uint64(0)
	view, found, err := p.PlaylistContext(context.Background(), "video.m3u8", PlaylistRequest{
		PlaylistOptions: PlaylistOptions{Skip: true},
		MSN:             &old,
	})
	if err != nil || !found {
		t.Fatalf("old MSN: found=%v err=%v", found, err)
	}
	if strings.Contains(string(view.Body), "#EXT-X-SKIP:") {
		t.Fatalf("old MSN returned a delta update instead of the full playlist:\n%s", view.Body)
	}
}

func TestPartAssetUsesParentSegmentGracePeriod(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p, err := New(Config{
		Dir:          t.TempDir(),
		PlaylistSize: 1,
		Grace:        30 * time.Second,
		Now:          c.now,
		LLHLS:        true,
		PartTarget:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.AddTrack(Track{Name: "video", Kind: KindVideo}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := p.PublishInit("video", []byte("init")); err != nil {
		t.Fatalf("PublishInit: %v", err)
	}
	publishLLSegment(t, p, 1)
	publishPart(t, p, 1, 0, true)
	publishLLSegment(t, p, 2)
	publishLLSegment(t, p, 3)

	partName := "video-part-000001-000.m4s"
	if _, ok := p.Asset(partName); !ok {
		t.Fatal("part was deleted as soon as its parent left the playlist")
	}
	c.add(31 * time.Second)
	publishLLSegment(t, p, 4)
	if _, ok := p.Asset(partName); ok {
		t.Fatal("part survived beyond its parent segment grace period")
	}
}

func TestCompletingStreamAbandonsPreloadWait(t *testing.T) {
	p := newLLPublisher(t, 8)
	publishLLSegment(t, p, 1)
	publishPart(t, p, 1, 0, true)

	done := make(chan error, 1)
	go func() {
		_, found, err := p.AssetContext(context.Background(), "video-part-000001-001.m4s")
		if err == nil && found {
			err = errors.New("abandoned hint was reported as an asset")
		}
		done <- err
	}()
	p.Complete()
	if err := <-done; err != nil {
		t.Fatalf("preload wait: %v", err)
	}
	pl, _ := p.Playlist("video.m3u8")
	if strings.Contains(string(pl), "#EXT-X-PRELOAD-HINT") {
		t.Fatalf("ended playlist still contains a preload hint:\n%s", pl)
	}
}
