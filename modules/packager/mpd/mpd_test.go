package mpd

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAnonymousAdaptationTrackKeysSurviveManifestReordering(t *testing.T) {
	manifest := func(first, second string) string {
		return `<?xml version="1.0"?><MPD mediaPresentationDuration="PT4S"><Period>` + first + second + `</Period></MPD>`
	}
	video := `<AdaptationSet contentType="video" mimeType="video/mp4"><SegmentTemplate initialization="v.mp4" media="v-$Number$.m4s" duration="2"/><Representation id="shared" codecs="avc1" width="1280" height="720" bandwidth="1000000"/></AdaptationSet>`
	audio := `<AdaptationSet contentType="audio" mimeType="audio/mp4" lang="yue"><Role value="main"/><SegmentTemplate initialization="a.mp4" media="a-$Number$.m4s" duration="2"/><Representation id="shared" codecs="mp4a" audioSamplingRate="48000" bandwidth="128000"/></AdaptationSet>`
	first, err := Parse([]byte(manifest(video, audio)), "https://example.com/live.mpd")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Parse([]byte(manifest(audio, video)), "https://example.com/live.mpd?token=rotated")
	if err != nil {
		t.Fatal(err)
	}
	keys := func(presentation *Presentation) map[ContentType]string {
		out := map[ContentType]string{}
		for _, representation := range presentation.Periods[0].Representations {
			out[representation.Type] = representation.TrackKey
		}
		return out
	}
	firstKeys, secondKeys := keys(first), keys(second)
	if firstKeys[TypeVideo] == "" || firstKeys[TypeVideo] != secondKeys[TypeVideo] || firstKeys[TypeAudio] != secondKeys[TypeAudio] {
		t.Fatalf("track keys changed after reordering: %#v != %#v", firstKeys, secondKeys)
	}
	if firstKeys[TypeVideo] == firstKeys[TypeAudio] {
		t.Fatal("duplicate representation IDs in different adaptation sets collided")
	}
}

const liveManifest = `<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" xmlns:cenc="urn:mpeg:cenc:2013"
     type="dynamic"
     availabilityStartTime="2026-01-01T00:00:00Z"
     publishTime="2026-01-01T00:10:00Z"
     minimumUpdatePeriod="PT2S"
     timeShiftBufferDepth="PT30S"
     suggestedPresentationDelay="PT6S"
     minBufferTime="PT4S">
  <UTCTiming schemeIdUri="urn:mpeg:dash:utc:http-xsdate:2014" value="https://time.example.com/utc"/>
  <BaseURL>https://cdn.example.com/live/</BaseURL>
  <Period id="p0" start="PT0S">
    <AdaptationSet contentType="video" mimeType="video/mp4" codecs="hvc1.1.6.L120">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc" cenc:default_KID="AABBCCDD-EEFF-0011-2233-445566778899"/>
      <SegmentTemplate timescale="90000" initialization="$RepresentationID$/init.mp4" media="$RepresentationID$/seg-$Number%05d$.m4s" startNumber="10">
        <SegmentTimeline>
          <S t="0" d="180000" r="-1"/>
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="v1" bandwidth="3000000" width="1920" height="1080"/>
    </AdaptationSet>
    <AdaptationSet contentType="audio" mimeType="audio/mp4" codecs="mp4a.40.2" lang="en">
      <ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
      <AudioChannelConfiguration schemeIdUri="urn:mpeg:dash:23003:3:audio_channel_configuration:2011" value="2"/>
      <SegmentTemplate timescale="48000" initialization="a/init.mp4" media="a/$Time$.m4s" duration="96000"/>
      <Representation id="a1" bandwidth="128000"/>
    </AdaptationSet>
  </Period>
</MPD>`

func parseLive(t *testing.T) *Presentation {
	t.Helper()
	p, err := Parse([]byte(liveManifest), "https://origin.example.com/x/manifest.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return p
}

func TestParseLiveAttributes(t *testing.T) {
	p := parseLive(t)
	if !p.Dynamic {
		t.Error("manifest should be dynamic")
	}
	if p.MinimumUpdatePeriod != 2*time.Second {
		t.Errorf("minimumUpdatePeriod = %v", p.MinimumUpdatePeriod)
	}
	if p.TimeShiftBufferDepth != 30*time.Second {
		t.Errorf("timeShiftBufferDepth = %v", p.TimeShiftBufferDepth)
	}
	if p.SuggestedPresentationDelay != 6*time.Second {
		t.Errorf("suggestedPresentationDelay = %v", p.SuggestedPresentationDelay)
	}
	want := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !p.AvailabilityStartTime.Equal(want) {
		t.Errorf("availabilityStartTime = %v, want %v", p.AvailabilityStartTime, want)
	}
	if len(p.UTCTimings) != 1 || p.UTCTimings[0].Scheme != "urn:mpeg:dash:utc:http-xsdate:2014" || p.UTCTimings[0].Value != "https://time.example.com/utc" {
		t.Fatalf("UTC timings = %#v", p.UTCTimings)
	}
}

func TestNormalizationResolvesInheritance(t *testing.T) {
	p := parseLive(t)
	reps := p.Periods[0].Representations
	if len(reps) != 2 {
		t.Fatalf("got %d representations, want 2", len(reps))
	}

	v := reps[0]
	if v.Type != TypeVideo || v.Codecs != "hvc1.1.6.L120" || v.Height != 1080 {
		t.Errorf("video representation = %+v", v)
	}
	if !v.Encrypted || v.Scheme != "cenc" {
		t.Errorf("video should be cenc encrypted, got %+v", v)
	}
	if v.DefaultKID != "aabbccddeeff00112233445566778899" {
		t.Errorf("default_KID = %s", v.DefaultKID)
	}
	if want := "https://cdn.example.com/live/v1/init.mp4"; v.Addressing.InitURL != want {
		t.Errorf("init url = %s, want %s", v.Addressing.InitURL, want)
	}
	if v.Addressing.Mode != AddressingTemplateTimeline {
		t.Errorf("video addressing = %s", v.Addressing.Mode)
	}

	a := reps[1]
	if a.Type != TypeAudio || a.Lang != "en" || a.AudioChannels != 2 {
		t.Errorf("audio representation = %+v", a)
	}
	if a.Addressing.Mode != AddressingTemplateDuration {
		t.Errorf("audio addressing = %s", a.Addressing.Mode)
	}
	if !a.Encrypted || a.DefaultKID != "" {
		t.Errorf("audio should be encrypted with no manifest KID, got %+v", a)
	}
}

func TestTimelineRepeatMinusOneExpandsToLiveEdge(t *testing.T) {
	p := parseLive(t)
	rep := p.Periods[0].Representations[0]

	now := p.AvailabilityStartTime.Add(21 * time.Second)
	segs, err := p.AvailableSegments(0, rep, now)
	if err != nil {
		t.Fatalf("AvailableSegments: %v", err)
	}
	if len(segs) != 10 {
		t.Fatalf("got %d segments, want 10", len(segs))
	}
	first, last := segs[0], segs[9]
	if first.Number != 10 {
		t.Errorf("first segment number = %d, want startNumber 10", first.Number)
	}
	if want := "https://cdn.example.com/live/v1/seg-00010.m4s"; first.URL != want {
		t.Errorf("first url = %s, want %s", first.URL, want)
	}
	if want := "https://cdn.example.com/live/v1/seg-00019.m4s"; last.URL != want {
		t.Errorf("last url = %s, want %s", last.URL, want)
	}
	if last.Time != 9*180000 {
		t.Errorf("last segment time = %d", last.Time)
	}
	if got := last.Seconds(rep.Addressing.Timescale); got != 2 {
		t.Errorf("segment duration = %v s, want 2", got)
	}
}

func TestLiveEdgeExcludesIncompleteSegment(t *testing.T) {
	p := parseLive(t)
	rep := p.Periods[0].Representations[0]
	now := p.AvailabilityStartTime.Add(3999 * time.Millisecond)
	segs, err := p.AvailableSegments(0, rep, now)
	if err != nil {
		t.Fatalf("AvailableSegments: %v", err)
	}
	if len(segs) != 1 {
		t.Fatalf("got %d segments, want 1 at t=3.999s", len(segs))
	}
}

func TestTimeShiftBufferTrimsExpiredSegments(t *testing.T) {
	p := parseLive(t)
	rep := p.Periods[0].Representations[0]
	now := p.AvailabilityStartTime.Add(120 * time.Second)
	segs, err := p.AvailableSegments(0, rep, now)
	if err != nil {
		t.Fatalf("AvailableSegments: %v", err)
	}
	if len(segs) != 15 {
		t.Fatalf("got %d segments, want 15 in a 30 s window", len(segs))
	}
	if segs[0].Number != 55 {
		t.Errorf("first surviving segment = %d, want 55", segs[0].Number)
	}
}

func TestTimeShiftBufferRetainsSegmentCrossingCutoff(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:        AddressingTemplateTimeline,
		Timescale:   1,
		StartNumber: 10,
		Timeline:    []TimelineEntry{{Duration: 4, Repeat: 2}},
	}}
	p := testPresentation(rep)
	p.Dynamic = true
	p.AvailabilityStartTime = time.Unix(1, 0)
	p.TimeShiftBufferDepth = 5 * time.Second

	segments, err := p.AvailableSegments(0, rep, p.AvailabilityStartTime.Add(12*time.Second))
	if err != nil {
		t.Fatalf("AvailableSegments: %v", err)
	}
	if len(segments) != 2 {
		t.Fatalf("segments=%d, want 2", len(segments))
	}
	if first := segments[0]; first.Number != 11 || first.Time != 4 {
		t.Fatalf("first segment=%+v", first)
	}
}

func TestDurationAddressingUsesTimeIdentifier(t *testing.T) {
	p := parseLive(t)
	rep := p.Periods[0].Representations[1]
	now := p.AvailabilityStartTime.Add(7 * time.Second)
	segs, err := p.AvailableSegments(0, rep, now)
	if err != nil {
		t.Fatalf("AvailableSegments: %v", err)
	}
	if len(segs) != 3 {
		t.Fatalf("got %d segments, want 3", len(segs))
	}
	if want := "https://cdn.example.com/live/a/0.m4s"; segs[0].URL != want {
		t.Errorf("first url = %s, want %s", segs[0].URL, want)
	}
	if want := "https://cdn.example.com/live/a/192000.m4s"; segs[2].URL != want {
		t.Errorf("third url = %s, want %s", segs[2].URL, want)
	}
}

func TestParseStaticFixtures(t *testing.T) {
	wantSegments := map[string]int{"h264": 2, "hevc": 3}
	for _, dir := range []string{"h264", "hevc"} {
		t.Run(dir, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", "testdata", "cenc", dir, "stream.mpd")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			p, err := Parse(raw, "https://example.com/"+dir+"/stream.mpd")
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if p.Dynamic {
				t.Error("fixture should be static")
			}
			reps := p.Periods[0].Representations
			if len(reps) != 2 {
				t.Fatalf("got %d representations, want 2", len(reps))
			}
			video := reps[0]
			if video.Type != TypeVideo {
				t.Errorf("first representation type = %s", video.Type)
			}
			if !video.Encrypted {
				t.Error("fixture is encrypted")
			}
			if video.DefaultKID != "" {
				t.Error("fixture MPD carries no default_KID; parser must not invent one")
			}
			if video.Addressing.Mode != AddressingList {
				t.Errorf("addressing = %s, want list", video.Addressing.Mode)
			}
			segs, err := p.AvailableSegments(0, video, time.Now())
			if err != nil {
				t.Fatalf("AvailableSegments: %v", err)
			}
			if len(segs) != wantSegments[dir] {
				t.Fatalf("got %d video segments, want %d", len(segs), wantSegments[dir])
			}
			want := "https://example.com/" + dir + "/chunk-stream0-00001.m4s"
			if segs[0].URL != want {
				t.Errorf("first url = %s, want %s", segs[0].URL, want)
			}
			if video.Addressing.InitURL != "https://example.com/"+dir+"/init-stream0.m4s" {
				t.Errorf("init url = %s", video.Addressing.InitURL)
			}
		})
	}
}

func testPresentation(rep Representation) *Presentation {
	return &Presentation{Periods: []Period{{Representations: []Representation{rep}}}}
}

func TestTimelineFiniteRepeatSharesExpansionBudget(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateTimeline, Timescale: 1, Timeline: []TimelineEntry{{Duration: 1, Repeat: 100000}}}}
	segs, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrExpansionLimit) || segs != nil {
		t.Fatalf("segments=%d err=%v", len(segs), err)
	}
}

func TestTimelineEntriesShareOneExpansionBudget(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateTimeline, Timescale: 1, Timeline: []TimelineEntry{{Duration: 1, Repeat: 49999}, {Time: 50000, Duration: 1, Repeat: 50000}}}}
	segs, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrExpansionLimit) || segs != nil {
		t.Fatalf("segments=%d err=%v", len(segs), err)
	}
}

func TestSegmentListUsesExpansionLimit(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingList, Timescale: 1, List: make([]string, 100001)}}
	segs, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrExpansionLimit) || segs != nil {
		t.Fatalf("segments=%d err=%v", len(segs), err)
	}
}

func TestTimelineRepeatOverflowIsRejected(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateTimeline, Timescale: 1, Timeline: []TimelineEntry{{Duration: 1, Repeat: math.MaxInt64}}}}
	_, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrAddressingOverflow) {
		t.Fatalf("err=%v", err)
	}
}

func TestTimelineTimeOverflowIsRejected(t *testing.T) {
	for _, entry := range []TimelineEntry{{Time: 1, Duration: math.MaxUint64, Repeat: 1}, {Time: math.MaxUint64, Duration: 1}} {
		rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateTimeline, Timescale: 1, Timeline: []TimelineEntry{entry}}}
		_, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
		if !errors.Is(err, ErrAddressingOverflow) {
			t.Fatalf("entry=%+v err=%v", entry, err)
		}
	}
}

func TestTimelineNumberOverflowAcrossEntriesIsRejected(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:        AddressingTemplateTimeline,
		Timescale:   1,
		StartNumber: math.MaxUint64,
		Timeline: []TimelineEntry{
			{Time: 0, Duration: 1},
			{Time: 1, Duration: 1},
		},
	}}
	_, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrAddressingOverflow) {
		t.Fatalf("err=%v", err)
	}
}

func TestDurationNumberOverflowIsRejected(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateDuration, Timescale: 1, Duration: 1, StartNumber: math.MaxUint64}}
	p := testPresentation(rep)
	p.MediaPresentationDuration = 2 * time.Second
	_, err := p.AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrAddressingOverflow) {
		t.Fatalf("err=%v", err)
	}
}

func TestPresentationOffsetOverflowIsRejected(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateDuration, Timescale: 1, Duration: 1, PresentationTimeOffset: math.MaxUint64}}
	p := testPresentation(rep)
	p.MediaPresentationDuration = time.Second
	_, err := p.AvailableSegments(0, rep, time.Time{})
	if !errors.Is(err, ErrAddressingOverflow) {
		t.Fatalf("err=%v", err)
	}
}

func TestParseDuration(t *testing.T) {
	cases := map[string]time.Duration{
		"PT0S":        0,
		"PT2S":        2 * time.Second,
		"PT4.0S":      4 * time.Second,
		"PT1M30S":     90 * time.Second,
		"PT1H2M3.5S":  time.Hour + 2*time.Minute + 3500*time.Millisecond,
		"P1DT2H":      26 * time.Hour,
		"PT0.5S":      500 * time.Millisecond,
		"-PT1S":       -time.Second,
		"PT1H":        time.Hour,
		"PT133.333S":  133333 * time.Millisecond,
		"P0Y0M0DT10S": 0,
	}
	for in, want := range cases {
		got, err := ParseDuration(in)
		if in == "P0Y0M0DT10S" {
			if err == nil {
				t.Errorf("ParseDuration(%q) should reject years/months", in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDuration(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseDuration(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"", "2S", "PT", "PTS", "PT1X", "P"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) should fail", bad)
		}
	}
}

func TestRejectsUnsupportedManifests(t *testing.T) {
	cases := map[string]string{
		"no period": `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static"></MPD>`,
		"dynamic without availabilityStartTime": `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="dynamic">
			<Period><AdaptationSet contentType="video"><Representation id="v"/></AdaptationSet></Period></MPD>`,
		"segment base": `<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static"><Period>
			<AdaptationSet contentType="video"><Representation id="v" bandwidth="1">
			<SegmentBase indexRange="0-100"/></Representation></AdaptationSet></Period></MPD>`,
		"not an mpd": `<Playlist/>`,
		"malformed":  `<MPD><Period>`,
	}
	for name, doc := range cases {
		if _, err := Parse([]byte(doc), "https://example.com/x.mpd"); err == nil {
			t.Errorf("%s: expected parse to fail", name)
		}
	}
}

func TestTrickModeMarkedOnlyByEssentialPropertyIsExcluded(t *testing.T) {
	raw := []byte(`<?xml version="1.0"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT10S">
  <Period id="0">
    <AdaptationSet id="1" contentType="video" mimeType="video/mp4" codecs="hvc1.1.6.L123.b0">
      <SegmentTemplate timescale="1000" duration="2000" initialization="v/init.m4s" media="v/$Number$.m4s"/>
      <Representation id="main" bandwidth="5000000" width="1920" height="1080"/>
    </AdaptationSet>
    <AdaptationSet id="2" contentType="video" mimeType="video/mp4" codecs="hvc1.1.6.L123.b0" codingDependency="false">
      <EssentialProperty schemeIdUri="http://dashif.org/guidelines/trickmode" value="1"/>
      <SegmentTemplate timescale="1000" duration="2000" initialization="t/init.m4s" media="t/$Number$.m4s"/>
      <Representation id="trick" bandwidth="9000000" width="1920" height="1080"/>
    </AdaptationSet>
  </Period>
</MPD>`)
	pres, err := Parse(raw, "https://example.com/stream.mpd")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, rep := range pres.Periods[0].Representations {
		if rep.ID == "trick" && !rep.Trick {
			t.Fatal("an i-frame-only track marked by EssentialProperty was not recognized as trick play; it would be selected as the main video and play as a slideshow")
		}
		if rep.ID == "main" && rep.Trick {
			t.Fatal("the main video was mistaken for a trick track")
		}
	}
}
