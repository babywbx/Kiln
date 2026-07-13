package packager

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

type abrFixtureOrigin struct{ base *liveOrigin }

func (origin abrFixtureOrigin) Fetch(ctx context.Context, rawURL string) ([]byte, string, error) {
	if strings.Contains(rawURL, "stream.mpd") {
		manifest := fmt.Sprintf(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static">
  <Period id="0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4" codecs="hvc1.1.6.L60.90">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
      <SegmentTemplate timescale="%d" initialization="v/init.m4s" media="v/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="%d" r="2"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v720" bandwidth="2500000" width="1280" height="720"/>
      <Representation id="v1080" bandwidth="6000000" width="1920" height="1080"/>
    </AdaptationSet>
    <AdaptationSet id="audio" contentType="audio" mimeType="audio/mp4" codecs="mp4a.40.2" lang="yue">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
      <SegmentTemplate timescale="%d" initialization="a/init.m4s" media="a/seg-$Number$.m4s" startNumber="1">
        <SegmentTimeline><S t="0" d="%d" r="2"/></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="a0" bandwidth="128000"/>
    </AdaptationSet>
  </Period>
</MPD>`, liveTimescale, liveSegTicks, liveTimescale, liveSegTicks)
		return []byte(manifest), rawURL, nil
	}
	return origin.base.Fetch(ctx, rawURL)
}

func TestNativePublishesEveryPlannedABRVariant(t *testing.T) {
	origin := newLiveOrigin(t)
	native, err := StartNative(context.Background(), Options{
		ManifestURL: "https://origin.example.com/live/stream.mpd",
		Dir:         t.TempDir(), Keys: keys(t), Fetcher: abrFixtureOrigin{base: origin},
		PreferHeight: 1080, PlaylistSize: 4, StartSegments: 1,
		LLHLS: true, PartTarget: 500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartNative: %v", err)
	}
	t.Cleanup(func() { _ = native.Stop() })

	master, ok := native.Publication().Playlist("master.m3u8")
	if !ok {
		t.Fatal("master playlist missing")
	}
	text := string(master)
	if !strings.Contains(text, "video-720p.m3u8") || !strings.Contains(text, "video-main.m3u8") || strings.Count(text, "#EXT-X-STREAM-INF:") != 2 {
		t.Fatalf("ABR master:\n%s", text)
	}
	media, ok := native.Publication().Playlist("video-main.m3u8")
	if !ok || !strings.Contains(string(media), "#EXT-X-PART:") || !strings.Contains(string(media), "#EXT-X-PRELOAD-HINT:") {
		t.Fatalf("LL-HLS media playlist:\n%s", media)
	}
	if stats := native.Stats(); stats.VideoTracks != 2 || stats.PartsPublished == 0 {
		t.Fatalf("native stats = %+v, want two video tracks and published parts", stats)
	}
	waitForStaticCompletion(t, native)
	assertIncreasingPartSequences(t, native.Publication(), "video-main.m3u8")
}

func assertIncreasingPartSequences(t *testing.T, publication *hls.Publisher, playlistName string) {
	t.Helper()
	playlist, ok := publication.Playlist(playlistName)
	if !ok {
		t.Fatalf("playlist %s missing", playlistName)
	}
	var previous uint32
	count := 0
	for line := range strings.SplitSeq(string(playlist), "\n") {
		if !strings.HasPrefix(line, "#EXT-X-PART:") {
			continue
		}
		const marker = `URI="`
		start := strings.Index(line, marker)
		if start < 0 {
			t.Fatalf("part line has no URI: %s", line)
		}
		name := line[start+len(marker):]
		end := strings.IndexByte(name, '"')
		if end < 0 {
			t.Fatalf("part line has unterminated URI: %s", line)
		}
		path, ok := publication.Asset(name[:end])
		if !ok {
			t.Fatalf("part asset %s missing", name[:end])
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		decoded, decodeErr := mp4.DecodeFile(file)
		closeErr := file.Close()
		if decodeErr != nil {
			t.Fatalf("decode part %s: %v", name[:end], decodeErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		sequence := decoded.Segments[0].Fragments[0].Moof.Mfhd.SequenceNumber
		if count > 0 && sequence != previous+1 {
			t.Fatalf("part sequence moved from %d to %d", previous, sequence)
		}
		previous = sequence
		count++
	}
	if count < 2 {
		t.Fatalf("playlist %s exposed only %d parts", playlistName, count)
	}
}

func TestPlanBuildsAddressableABRLadderUpToPreferredHeight(t *testing.T) {
	t.Parallel()

	presentation := abrPresentation(
		videoRep("v2160", 2160, 18_000_000),
		videoRep("v1080-low", 1080, 4_000_000),
		videoRep("v1080", 1080, 6_000_000),
		videoRep("v720", 720, 2_500_000),
		videoRep("trick", 360, 200_000),
		abrAudioRep("a"),
	)
	presentation.Periods[0].Representations[4].Trick = true

	plan, err := PlanFromManifest(presentation, 1080)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if got, want := representationIDs(plan.Videos), []string{"v720", "v1080"}; !equalStrings(got, want) {
		t.Fatalf("ABR ladder IDs = %v, want %v", got, want)
	}
	if plan.Video.ID != "v1080" {
		t.Fatalf("primary video = %q, want v1080", plan.Video.ID)
	}
}

func TestPlanFallsBackToLowestVideoWhenPreferredHeightIsBelowLadder(t *testing.T) {
	t.Parallel()

	plan, err := PlanFromManifest(abrPresentation(
		videoRep("v1080", 1080, 6_000_000),
		videoRep("v720", 720, 2_500_000),
		abrAudioRep("a"),
	), 480)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if got, want := representationIDs(plan.Videos), []string{"v720"}; !equalStrings(got, want) {
		t.Fatalf("ABR ladder IDs = %v, want %v", got, want)
	}
}

func TestPlanIncludesSupportedSTPPTextAndReportsUnsupportedText(t *testing.T) {
	t.Parallel()

	supported := textRep("zh", "stpp.ttml.im1t", "zh")
	unsupported := textRep("wvtt", "wvtt", "en")
	plan, err := PlanFromManifest(abrPresentation(videoRep("v", 1080, 1), abrAudioRep("a"), supported, unsupported), 0)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if got, want := representationIDs(plan.Texts), []string{"zh"}; !equalStrings(got, want) {
		t.Fatalf("text tracks = %v, want %v", got, want)
	}
	if got, want := plan.SkippedText, []string{"wvtt"}; !equalStrings(got, want) {
		t.Fatalf("skipped text = %v, want %v", got, want)
	}
}

func abrPresentation(representations ...mpd.Representation) *mpd.Presentation {
	return &mpd.Presentation{Periods: []mpd.Period{{Representations: representations}}}
}

func videoRep(id string, height, bandwidth int) mpd.Representation {
	return mpd.Representation{
		ID: id, Type: mpd.TypeVideo, Codecs: "hvc1.1.6.L123", Height: height,
		Width: height * 16 / 9, Bandwidth: bandwidth,
		Addressing: mpd.Addressing{Mode: mpd.AddressingTemplateDuration},
	}
}

func abrAudioRep(id string) mpd.Representation {
	return mpd.Representation{
		ID: id, Type: mpd.TypeAudio, Codecs: "mp4a.40.2", Group: id,
		Addressing: mpd.Addressing{Mode: mpd.AddressingTemplateDuration},
	}
}

func textRep(id, codec, language string) mpd.Representation {
	return mpd.Representation{
		ID: id, Type: mpd.TypeText, Codecs: codec, Lang: language,
		Addressing: mpd.Addressing{Mode: mpd.AddressingTemplateDuration},
	}
}

func representationIDs(representations []mpd.Representation) []string {
	ids := make([]string, 0, len(representations))
	for _, representation := range representations {
		ids = append(ids, representation.ID)
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
