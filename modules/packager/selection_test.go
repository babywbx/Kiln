package packager

import (
	"context"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

type inspectionFetcher struct{ body string }

func (f inspectionFetcher) Fetch(context.Context, string) ([]byte, string, error) {
	return []byte(f.body), "https://cdn.example/final/manifest.mpd?token=secret", nil
}

func TestPlanSelectionDistinguishesFrameRatesAndHonorsDefaults(t *testing.T) {
	addressing := mpd.Addressing{Mode: mpd.AddressingTemplateDuration}
	presentation := &mpd.Presentation{Periods: []mpd.Period{{Representations: []mpd.Representation{
		{ID: "v720", TrackKey: "v720", Group: "v", Type: mpd.TypeVideo, Codecs: "avc1.4d401f", Width: 1280, Height: 720, FrameRate: "25", Bandwidth: 2_000_000, Addressing: addressing},
		{ID: "v1080-25", TrackKey: "v1080-25", Group: "v", Type: mpd.TypeVideo, Codecs: "avc1.640028", Width: 1920, Height: 1080, FrameRate: "25", Bandwidth: 4_000_000, Addressing: addressing},
		{ID: "v1080-50", TrackKey: "v1080-50", Group: "v", Type: mpd.TypeVideo, Codecs: "avc1.64002a", Width: 1920, Height: 1080, FrameRate: "50", Bandwidth: 7_000_000, Addressing: addressing},
		{ID: "a-en", TrackKey: "a-en", Group: "a-en", Type: mpd.TypeAudio, Codecs: "mp4a.40.2", Lang: "en", Bandwidth: 128_000, Addressing: addressing},
		{ID: "a-yue", TrackKey: "a-yue", Group: "a-yue", Type: mpd.TypeAudio, Codecs: "mp4a.40.2", Lang: "yue", Roles: []string{"main"}, Bandwidth: 128_000, Addressing: addressing},
		{ID: "s-zh", TrackKey: "s-zh", Group: "s-zh", Type: mpd.TypeText, Codecs: "stpp", Lang: "zh", Addressing: addressing},
	}}}}
	selection := config.TrackSelection{
		Video:     config.VideoSelection{Mode: "cap", Track: config.TrackSelector{Key: "v1080-50"}},
		Audio:     config.AudioSelection{Mode: "prefer", Track: config.TrackSelector{Key: "a-yue"}},
		Subtitles: config.SubtitleSelection{Mode: "prefer", Track: config.TrackSelector{Key: "s-zh"}},
	}
	plan, err := PlanFromManifestWithSelection(presentation, 0, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Videos) != 3 || plan.Video.ID != "v1080-50" {
		t.Fatalf("videos = %#v, primary = %s", plan.Videos, plan.Video.ID)
	}
	if plan.DefaultAudioKey != "a-yue" || plan.DefaultTextKey != "s-zh" {
		t.Fatalf("defaults = %q / %q", plan.DefaultAudioKey, plan.DefaultTextKey)
	}
}

func TestPlanSelectionRejectsDisappearedExplicitTrack(t *testing.T) {
	presentation := &mpd.Presentation{Periods: []mpd.Period{{Representations: []mpd.Representation{
		{ID: "v", Group: "v", Type: mpd.TypeVideo, Codecs: "avc1", Height: 720, Addressing: mpd.Addressing{Mode: mpd.AddressingTemplateDuration}},
		{ID: "a", Group: "a", Type: mpd.TypeAudio, Codecs: "mp4a", Addressing: mpd.Addressing{Mode: mpd.AddressingTemplateDuration}},
	}}}}
	_, err := PlanFromManifestWithSelection(presentation, 0, config.TrackSelection{Audio: config.AudioSelection{Mode: "prefer", Track: config.TrackSelector{RepresentationID: "gone"}}})
	if err == nil {
		t.Fatal("expected an explicit missing-track error")
	}
}

func TestInspectManifestListsUnsupportedTracksAndChecksKIDs(t *testing.T) {
	mpdBody := `<?xml version="1.0"?><MPD mediaPresentationDuration="PT10S"><Period><AdaptationSet id="video" contentType="video" mimeType="video/mp4">
<Representation id="v1080" bandwidth="5000000" width="1920" height="1080" frameRate="50" codecs="avc1.640028"><SegmentBase/></Representation>
</AdaptationSet><AdaptationSet id="audio" contentType="audio" mimeType="audio/mp4" lang="yue"><Role value="main"/>
<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc" default_KID="00112233-4455-6677-8899-aabbccddeeff"/>
<SegmentTemplate initialization="a-init.mp4" media="a-$Number$.m4s" duration="2" timescale="1"/>
<Representation id="a-yue" bandwidth="192000" codecs="mp4a.40.2" audioSamplingRate="48000"/></AdaptationSet></Period></MPD>`
	inspection, err := InspectManifest(context.Background(), inspectionFetcher{body: mpdBody}, "https://entry.example/live.mpd", 0, config.TrackSelection{}, []config.KeyPair{{KID: "00112233445566778899aabbccddeeff", Key: strings.Repeat("0", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Videos) != 1 || inspection.Videos[0].NativeSupported || len(inspection.Audios) != 1 {
		t.Fatalf("inspection = %#v", inspection)
	}
	if inspection.KeyStatus != "matched" || inspection.Recommendation.AudioKey == "" {
		t.Fatalf("key status/recommendation = %#v", inspection)
	}
}

func TestInspectManifestDoesNotClaimKeysMatchWithoutDefaultKID(t *testing.T) {
	mpdBody := `<?xml version="1.0"?><MPD mediaPresentationDuration="PT4S"><Period><AdaptationSet contentType="video" mimeType="video/mp4">
<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>
<SegmentTemplate initialization="v.mp4" media="v-$Number$.m4s" duration="2"/><Representation id="v" codecs="avc1" width="640" height="360"/></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><SegmentTemplate initialization="a.mp4" media="a-$Number$.m4s" duration="2"/><Representation id="a" codecs="mp4a"/></AdaptationSet></Period></MPD>`
	inspection, err := InspectManifest(context.Background(), inspectionFetcher{body: mpdBody}, "https://example.com/live.mpd", 0, config.TrackSelection{}, []config.KeyPair{{KID: strings.Repeat("1", 32), Key: strings.Repeat("2", 32)}})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.KeyStatus != "unknown" {
		t.Fatalf("key status = %q, want unknown", inspection.KeyStatus)
	}
}

func TestExplicitUnsupportedAudioForcesCompatibilityInsteadOfSilentFallback(t *testing.T) {
	addressing := mpd.Addressing{Mode: mpd.AddressingTemplateDuration}
	presentation := &mpd.Presentation{Periods: []mpd.Period{{Representations: []mpd.Representation{
		{ID: "v", TrackKey: "v", Group: "v", Type: mpd.TypeVideo, Codecs: "avc1", Height: 720, Addressing: addressing},
		{ID: "aac", TrackKey: "aac", Group: "a1", Type: mpd.TypeAudio, Codecs: "mp4a", Addressing: addressing},
		{ID: "eac3", TrackKey: "eac3", Group: "a2", Type: mpd.TypeAudio, Codecs: "ec-3", Addressing: addressing},
	}}}}
	plan, err := PlanFromManifestWithSelection(presentation, 0, config.TrackSelection{Audio: config.AudioSelection{Mode: "prefer", Track: config.TrackSelector{Key: "eac3"}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Native() || plan.Engine != EngineFFmpegCopy {
		t.Fatalf("plan = %#v, want explicit compatibility fallback", plan)
	}
}
