package egress

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFilterMPDForPackPrefer1080(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD>
  <Period>
    <BaseURL>6/</BaseURL>
    <AdaptationSet contentType="video">
      <Representation id="v5000000" bandwidth="5000000" width="1920" height="1080" codecs="hev1"/>
      <Representation id="v1500000" bandwidth="1500000" width="1024" height="576" codecs="hev1"/>
      <Representation id="v5000000-TrickMode" bandwidth="2500000" width="1920" height="1080" codecs="hev1" maxPlayoutRate="400"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio">
      <Representation id="au1" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	out, note, err := FilterMPDForPack(mpd, "http://cdn.example/live/index.mpd?x=1", 1080)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, `id="v5000000"`) {
		t.Fatalf("want 1080 kept: %s note=%s", out, note)
	}
	if contains(out, "TrickMode") || contains(out, `id="v1500000"`) {
		t.Fatalf("trick/low should drop: %s", out)
	}
	if !contains(out, "au1") {
		t.Fatalf("audio missing: %s", out)
	}
	if !contains(out, "http://cdn.example/live/6/") {
		t.Fatalf("resolved baseurl missing: %s", out)
	}
}

// An AdaptationSet whose Representations were all dropped must go too: ffmpeg's
// DASH demuxer stalls on an empty one and never emits a segment.
func TestFilterMPDForPackDropsEmptyAdaptationSets(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="dynamic">
  <Period>
    <BaseURL>6/</BaseURL>
    <AdaptationSet id="1" contentType="video">
      <SegmentTemplate media="$RepresentationID$/D$Time$.cmfv"><SegmentTimeline><S t="0" d="100"/></SegmentTimeline></SegmentTemplate>
      <Representation id="v5000000" bandwidth="5000000" width="1920" height="1080" codecs="hev1"/>
    </AdaptationSet>
    <AdaptationSet id="2" contentType="video">
      <SegmentTemplate media="$RepresentationID$/D$Time$.cmfi"><SegmentTimeline><S t="0" d="100"/></SegmentTimeline></SegmentTemplate>
      <Representation id="v5000000-TrickMode" bandwidth="2500000" width="1920" height="1080" codecs="hev1" maxPlayoutRate="400"/>
    </AdaptationSet>
    <AdaptationSet id="3" contentType="audio">
      <Representation id="a128000" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="32000"/>
    </AdaptationSet>
    <AdaptationSet id="4" contentType="audio">
      <Representation id="a128001" bandwidth="128001" codecs="mp4a.40.2" audioSamplingRate="32000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	out, _, err := FilterMPDForPack(mpd, "http://cdn.example/live/index.mpd", 1080)
	if err != nil {
		t.Fatal(err)
	}
	sets := adaptationSetRe.FindAllString(out, -1)
	if len(sets) != 2 {
		t.Fatalf("want 2 adaptation sets (one video, one audio), got %d:\n%s", len(sets), out)
	}
	for _, s := range sets {
		if !repTagRe.MatchString(s) {
			t.Fatalf("empty adaptation set survived:\n%s", s)
		}
	}
	if contains(out, "TrickMode") || contains(out, `id="a128001"`) {
		t.Fatalf("dropped representations still present:\n%s", out)
	}
}

func TestPickVideoHighestWhenPreferZero(t *testing.T) {
	videos := []videoRep{
		{id: "a", height: 576, bandwidth: 1},
		{id: "b", height: 2160, bandwidth: 15},
		{id: "c", height: 1080, bandwidth: 5, trick: true},
	}
	v := pickVideo(videos, 0)
	if v == nil || v.id != "b" {
		t.Fatalf("%+v", v)
	}
}

func TestPickStreamsDynamicAndIndex(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD type="dynamic">
  <Period>
    <AdaptationSet>
      <Representation id="vlow" bandwidth="1000" width="640" height="360" codecs="hev1"/>
      <Representation id="vhi" bandwidth="5000" width="1920" height="1080" codecs="hev1"/>
      <Representation id="a1" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
    </AdaptationSet>
  </Period>
</MPD>`
	p := PickStreams(mpd, 1080)
	if !p.Dynamic {
		t.Fatal("want dynamic")
	}
	if p.VideoIndex != 1 || p.VideoID != "vhi" {
		t.Fatalf("pick=%+v", p)
	}
	if p.AudioIndex != 0 {
		t.Fatalf("audio index=%d", p.AudioIndex)
	}
}

func TestExplicitStreamSelectionIsUsedByFilterAndRemoteMapping(t *testing.T) {
	mpd := `<MPD type="dynamic"><Period><AdaptationSet contentType="video">
<Representation id="v25" bandwidth="4000" width="1920" height="1080" frameRate="25" codecs="hev1"/>
<Representation id="v50" bandwidth="7000" width="1920" height="1080" frameRate="50" codecs="hev1"/>
</AdaptationSet><AdaptationSet contentType="audio">
<Representation id="a-en" bandwidth="128000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
<Representation id="a-yue" bandwidth="192000" codecs="mp4a.40.2" audioSamplingRate="48000"/>
</AdaptationSet></Period></MPD>`
	out, _, err := FilterMPDForPackWithSelection(mpd, "https://example.com/live.mpd", 0, "v50", "a-yue")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, `id="v50"`) || !contains(out, `id="a-yue"`) || contains(out, `id="v25"`) || contains(out, `id="a-en"`) {
		t.Fatalf("unexpected filtered MPD: %s", out)
	}
	pick, err := PickStreamsWithSelection(mpd, 0, "v50", "a-yue")
	if err != nil {
		t.Fatal(err)
	}
	if pick.VideoIndex != 1 || pick.AudioIndex != 1 {
		t.Fatalf("pick = %+v", pick)
	}
	if _, err := PickStreamsWithSelection(mpd, 0, "missing", "a-yue"); err == nil {
		t.Fatal("expected missing explicit representation to fail")
	}
}

func TestExplicitStreamSelectionRejectsDuplicateRepresentationIDs(t *testing.T) {
	mpd := `<MPD><Period><AdaptationSet contentType="video"><Representation id="duplicate" width="1280" height="720" codecs="avc1"/></AdaptationSet>
<AdaptationSet contentType="video"><Representation id="duplicate" width="1920" height="1080" codecs="avc1"/></AdaptationSet>
<AdaptationSet contentType="audio"><Representation id="audio" codecs="mp4a" audioSamplingRate="48000"/></AdaptationSet></Period></MPD>`
	if _, err := PickStreamsWithSelection(mpd, 0, "duplicate", "audio"); err == nil {
		t.Fatal("expected duplicate representation IDs to be rejected")
	}
	if _, _, err := FilterMPDForPackWithSelection(mpd, "https://example.com/live.mpd", 0, "duplicate", "audio"); err == nil {
		t.Fatal("expected the local filtered MPD to reject the same ambiguity")
	}
}

func TestReadyPlaylistRequiresPlayableSegment(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(dir, "index.m3u8")
	seg0 := filepath.Join(dir, "seg_00000.ts")
	seg1 := filepath.Join(dir, "seg_00001.ts")
	write := func(t *testing.T, path string, b []byte) {
		t.Helper()
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(t, index, []byte("#EXTM3U\n#EXTINF:0.18,\nseg_00000.ts\n"))
	write(t, seg0, make([]byte, 1024))
	if readyPlaylist(index, dir) {
		t.Fatal("tiny partial should not be ready")
	}

	// One full segment still means the packager may have starved right after.
	write(t, index, []byte("#EXTM3U\n#EXTINF:2.0,\nseg_00000.ts\n"))
	write(t, seg0, make([]byte, 64*1024))
	if readyPlaylist(index, dir) {
		t.Fatal("a lone segment must not count as ready")
	}

	write(t, index, []byte("#EXTM3U\n#EXTINF:2.0,\nseg_00000.ts\n#EXTINF:2.0,\nseg_00001.ts\n"))
	write(t, seg1, make([]byte, 64*1024))
	if !readyPlaylist(index, dir) {
		t.Fatal("want ready once a second segment lands")
	}
}

func TestTrimSegmentTimelinesKeepsTail(t *testing.T) {
	mpd := `<?xml version="1.0"?>
<MPD>
  <Period>
    <SegmentTemplate>
      <SegmentTimeline>
        <S t="1" d="10"/>
        <S t="11" d="10"/>
        <S t="21" d="10"/>
        <S t="31" d="10"/>
        <S t="41" d="10"/>
      </SegmentTimeline>
    </SegmentTemplate>
  </Period>
</MPD>`
	out, dropped := trimSegmentTimelines(mpd, 2)
	if dropped != 3 {
		t.Fatalf("dropped=%d want 3", dropped)
	}
	if contains(out, `t="1"`) || contains(out, `t="11"`) || contains(out, `t="21"`) {
		t.Fatalf("old S kept: %s", out)
	}
	if !contains(out, `t="31"`) || !contains(out, `t="41"`) {
		t.Fatalf("tail missing: %s", out)
	}
}
