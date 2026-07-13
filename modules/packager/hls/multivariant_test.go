package hls

import (
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/timedmeta"
)

func TestMasterPublishesEveryVideoVariantAndSubtitleRendition(t *testing.T) {
	t.Parallel()

	publisher, err := New(Config{Dir: t.TempDir(), PlaylistSize: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tracks := []Track{
		{Name: "video-720", Kind: KindVideo, Codec: "hvc1", Bandwidth: 2_000_000, Width: 1280, Height: 720},
		{Name: "video-main", Kind: KindVideo, Codec: "hvc1", Bandwidth: 6_000_000, Width: 1920, Height: 1080},
		{Name: "audio-main", Kind: KindAudio, Codec: "mp4a.40.2", Bandwidth: 128_000, Lang: "yue"},
		{Name: "subtitle-zh-hant", Kind: KindSubtitle, Lang: "zh-Hant", Label: "繁體中文"},
	}
	for _, track := range tracks {
		if err := publisher.AddTrack(track); err != nil {
			t.Fatalf("AddTrack(%s): %v", track.Name, err)
		}
		if track.Kind != KindSubtitle {
			if err := publisher.PublishInit(track.Name, []byte("init")); err != nil {
				t.Fatalf("PublishInit(%s): %v", track.Name, err)
			}
		}
	}
	for _, name := range []string{"video-720", "video-main", "audio-main"} {
		if err := publisher.PublishSegment(Publication{Track: name, Seq: 1, Duration: 2}, []byte("segment")); err != nil {
			t.Fatalf("PublishSegment(%s): %v", name, err)
		}
	}

	master, ok := publisher.Playlist(MasterName)
	if !ok {
		t.Fatal("master was not published while optional subtitle was still empty")
	}
	text := string(master)
	for _, want := range []string{
		`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subtitles",NAME="繁體中文",LANGUAGE="zh-Hant"`,
		`SUBTITLES="subtitles"`,
		"video-720.m3u8", "video-main.m3u8",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("master missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "#EXT-X-STREAM-INF:") != 2 {
		t.Fatalf("master variants:\n%s", text)
	}
}

func TestSubtitleSegmentsArePublishedAsWebVTTWithoutInitMap(t *testing.T) {
	t.Parallel()

	publisher, err := New(Config{Dir: t.TempDir(), PlaylistSize: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := publisher.AddTrack(Track{Name: "subtitle-en", Kind: KindSubtitle, Lang: "en"}); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}
	if err := publisher.PublishSegment(Publication{Track: "subtitle-en", Seq: 1, Duration: 2}, []byte("WEBVTT\n\n")); err != nil {
		t.Fatalf("PublishSegment: %v", err)
	}
	playlist, ok := publisher.Playlist("subtitle-en.m3u8")
	if !ok {
		t.Fatal("subtitle playlist missing")
	}
	if text := string(playlist); !strings.Contains(text, "subtitle-en-000001.vtt") || strings.Contains(text, "#EXT-X-MAP") {
		t.Fatalf("subtitle playlist:\n%s", text)
	}
	if path, ok := publisher.Asset("subtitle-en-000001.vtt"); !ok || !strings.HasSuffix(path, ".vtt") {
		t.Fatalf("subtitle asset = %q, %v", path, ok)
	}
}

func TestPublicationCarriesDateRangeBeforeItsMediaSegment(t *testing.T) {
	t.Parallel()

	publisher, err := New(Config{Dir: t.TempDir(), PlaylistSize: 4})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := publisher.AddTrack(Track{Name: "video", Kind: KindVideo}); err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishInit("video", []byte("init")); err != nil {
		t.Fatal(err)
	}
	dateRange := timedmeta.DateRange{
		ID: "splice-7", Class: "com.apple.hls.scte35",
		StartDate: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	tag := dateRange.MarshalTag()
	if err := publisher.PublishSegment(Publication{
		Track: "video", Seq: 1, Duration: 2, At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		DateRanges: []timedmeta.DateRange{dateRange},
	}, []byte("segment")); err != nil {
		t.Fatal(err)
	}
	playlist, _ := publisher.Playlist("video.m3u8")
	text := string(playlist)
	if dateRange, segment := strings.Index(text, tag), strings.Index(text, "video-000001.m4s"); dateRange < 0 || segment < 0 || dateRange > segment {
		t.Fatalf("date range was not emitted before segment:\n%s", text)
	}
}

func TestSCTE35DateRangeIsMergedAndReplicatedAcrossVideoVariants(t *testing.T) {
	t.Parallel()

	publisher, err := New(Config{Dir: t.TempDir(), PlaylistSize: 4, Static: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, name := range []string{"video-main", "video-alt"} {
		if err := publisher.AddTrack(Track{Name: name, Kind: KindVideo}); err != nil {
			t.Fatal(err)
		}
		if err := publisher.PublishInit(name, []byte("init")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	end := start.Add(2 * time.Second)
	out := timedmeta.DateRange{
		ID: "scte35-7", Class: "com.apple.hls.scte35", StartDate: start, SCTE35Out: "0xFC01",
	}
	in := timedmeta.DateRange{
		ID: "scte35-7", Class: "com.apple.hls.scte35", StartDate: end, SCTE35In: "0xFC02",
	}
	for _, publication := range []Publication{
		{Track: "video-alt", Seq: 1, Duration: 2, At: start},
		{Track: "video-main", Seq: 1, Duration: 2, At: start, DateRanges: []timedmeta.DateRange{out}},
		{Track: "video-alt", Seq: 2, Duration: 2, At: end},
		{Track: "video-main", Seq: 2, Duration: 2, At: end, DateRanges: []timedmeta.DateRange{in}},
	} {
		if err := publisher.PublishSegment(publication, []byte("segment")); err != nil {
			t.Fatalf("PublishSegment(%s/%d): %v", publication.Track, publication.Seq, err)
		}
	}
	for _, name := range []string{"video-main.m3u8", "video-alt.m3u8"} {
		playlist, ok := publisher.Playlist(name)
		if !ok {
			t.Fatalf("playlist %s missing", name)
		}
		text := string(playlist)
		if strings.Count(text, `ID="scte35-7"`) != 1 ||
			!strings.Contains(text, `START-DATE="2026-07-13T12:00:00.000Z"`) ||
			!strings.Contains(text, `END-DATE="2026-07-13T12:00:02.000Z"`) ||
			!strings.Contains(text, `SCTE35-OUT="0xFC01"`) ||
			!strings.Contains(text, `SCTE35-IN="0xFC02"`) {
			t.Fatalf("playlist %s did not contain one merged range:\n%s", name, text)
		}
	}
}
