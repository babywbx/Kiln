package packager

import (
	"slices"
	"testing"

	"github.com/babywbx/kiln/modules/packager/mpd"
)

func essentialPresentation(videoEssential, audioEssential []string) *mpd.Presentation {
	addressing := mpd.Addressing{
		Mode:      mpd.AddressingTemplateTimeline,
		InitURL:   "https://origin.example.com/live/$RepresentationID$/init.mp4",
		Media:     "https://origin.example.com/live/$RepresentationID$/$Number$.m4s",
		Timescale: 90000,
	}
	return &mpd.Presentation{
		Dynamic: true,
		Periods: []mpd.Period{{
			Representations: []mpd.Representation{
				{
					ID: "tiled", Group: "1", Type: mpd.TypeVideo, Codecs: "avc1.640028",
					Bandwidth: 6000000, Width: 3840, Height: 2160,
					Essential: videoEssential, Addressing: addressing,
				},
				{
					ID: "plain", Group: "2", Type: mpd.TypeVideo, Codecs: "avc1.640028",
					Bandwidth: 3000000, Width: 1920, Height: 1080,
					Addressing: addressing,
				},
				{
					ID: "a0", Group: "3", Type: mpd.TypeAudio, Codecs: "mp4a.40.2",
					Bandwidth: 128000, Lang: "zho",
					Essential: audioEssential, Addressing: addressing,
				},
			},
		}},
	}
}

func TestSpatialTiledRepresentationIsNotSelected(t *testing.T) {
	srd := []string{"urn:mpeg:dash:srd:2014"}
	plan, err := PlanFromManifest(essentialPresentation(srd, nil), 0)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if !plan.Native() {
		t.Fatalf("engine = %s (%s), want the untiled video to carry the plan", plan.Engine, plan.Reason)
	}
	if plan.Video.ID != "plain" {
		t.Fatalf("video = %s, want plain: an SRD tile is a fragment of the picture, not the picture", plan.Video.ID)
	}
	if len(plan.UnknownEssential) != 0 {
		t.Errorf("unknown essential = %v, want none once the tile is dropped", plan.UnknownEssential)
	}
}

func TestSpatialTiledAudioIsNotSelected(t *testing.T) {
	srd := []string{"urn:mpeg:dash:srd:2014"}
	plan, err := PlanFromManifest(essentialPresentation(nil, srd), 0)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if plan.Native() {
		t.Fatalf("engine = %s, want no native plan when the only audio is unusable", plan.Engine)
	}
	if plan.Reason != ReasonNoAudio {
		t.Errorf("reason = %s, want %s", plan.Reason, ReasonNoAudio)
	}
}

func TestHDRColourPropertiesDoNotWarn(t *testing.T) {
	cicp := []string{
		"urn:mpeg:mpegb:cicp:transfercharacteristics",
		"urn:mpeg:mpegb:cicp:matrixcoefficients",
	}
	plan, err := PlanFromManifest(essentialPresentation(cicp, nil), 2160)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if plan.Video.ID != "tiled" {
		t.Fatalf("video = %s, want the 2160p rep: colour signalling is not a reason to drop it", plan.Video.ID)
	}
	if len(plan.UnknownEssential) != 0 {
		t.Errorf("unknown essential = %v, want none: these schemes are understood", plan.UnknownEssential)
	}
}

func TestUnrecognizedEssentialPropertyIsReported(t *testing.T) {
	plan, err := PlanFromManifest(essentialPresentation([]string{"urn:example:unheard-of:2030"}, nil), 2160)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	if !plan.Native() {
		t.Fatalf("engine = %s (%s), want an unknown scheme to be reported, not fatal", plan.Engine, plan.Reason)
	}
	if plan.Video.ID != "tiled" {
		t.Fatalf("video = %s, want the rep still selected", plan.Video.ID)
	}
	if !slices.Contains(plan.UnknownEssential, "urn:example:unheard-of:2030") {
		t.Errorf("unknown essential = %v, want the scheme surfaced to the operator", plan.UnknownEssential)
	}
}
