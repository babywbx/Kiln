package packager

import (
	"testing"

	"github.com/babywbx/kiln/modules/packager/mpd"
)

func TestTextRenditionsArePlannedWhenSupported(t *testing.T) {
	addressing := mpd.Addressing{Mode: mpd.AddressingTemplateDuration, Timescale: 1, Duration: 2}
	presentation := &mpd.Presentation{
		Periods: []mpd.Period{{
			Representations: []mpd.Representation{
				{ID: "video", Type: mpd.TypeVideo, Codecs: "hvc1", Addressing: addressing},
				{ID: "audio", Type: mpd.TypeAudio, Codecs: "mp4a", Group: "audio", Addressing: addressing},
				{ID: "sub-zh", Type: mpd.TypeText, Codecs: "stpp", Lang: "zh", Addressing: addressing},
				{ID: "sub-en", Type: mpd.TypeText, Codecs: "stpp", Lang: "en", Addressing: addressing},
			},
		}},
	}
	plan, err := PlanFromManifest(presentation, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Texts) != 2 || plan.Texts[0].ID != "sub-zh" || plan.Texts[1].ID != "sub-en" {
		t.Fatalf("planned text = %v", plan.Texts)
	}
	if len(plan.SkippedText) != 0 {
		t.Fatalf("skipped text = %v", plan.SkippedText)
	}
}
