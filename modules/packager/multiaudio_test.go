package packager

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/packager/hls"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

func TestEveryAudioLanguageIsPublished(t *testing.T) {
	origin := newLiveOrigin(t)
	origin.audios = []audioSet{
		{prefix: "a", lang: "zho", reps: []audioRep{{id: "a0", bandwidth: 32000}}},
		{prefix: "a2", lang: "eng", reps: []audioRep{{id: "a1", bandwidth: 32000}}},
	}
	n, _ := startLive(t, origin, newClock())

	if got := n.Stats().AudioTracks; got != 2 {
		t.Fatalf("published %d audio tracks, want 2", got)
	}
	for _, name := range []string{"audio-main.m3u8", "audio-eng.m3u8"} {
		pl, ok := n.Publication().Playlist(name)
		if !ok {
			t.Fatalf("no playlist %s", name)
		}
		if got := strings.Count(string(pl), ".m4s"); got != 3 {
			t.Errorf("%s carries %d segments, want 3:\n%s", name, got, pl)
		}
	}

	master, ok := n.Publication().Playlist(hls.MasterName)
	if !ok {
		t.Fatal("no master playlist")
	}
	got := string(master)

	if n := strings.Count(got, "#EXT-X-MEDIA:TYPE=AUDIO"); n != 2 {
		t.Fatalf("master advertises %d audio renditions, want 2:\n%s", n, got)
	}
	if n := strings.Count(got, "DEFAULT=YES"); n != 1 {
		t.Errorf("master has %d default audio renditions, want 1:\n%s", n, got)
	}
	for _, want := range []string{`LANGUAGE="zho"`, `LANGUAGE="eng"`, `URI="audio-main.m3u8"`, `URI="audio-eng.m3u8"`} {
		if !strings.Contains(got, want) {
			t.Errorf("master is missing %s:\n%s", want, got)
		}
	}
}

func TestEveryAudioLanguageAdvances(t *testing.T) {
	origin := newLiveOrigin(t)
	origin.audios = []audioSet{
		{prefix: "a", lang: "zho", reps: []audioRep{{id: "a0", bandwidth: 32000}}},
		{prefix: "a2", lang: "eng", reps: []audioRep{{id: "a1", bandwidth: 32000}}},
	}
	clock := newClock()
	n, _ := startLive(t, origin, clock)

	origin.grow(2)
	clock.advance(4 * time.Second)
	if err := n.advance(context.Background(), parseManifest(t, origin)); err != nil {
		t.Fatalf("advance: %v", err)
	}

	for _, ts := range n.audios {
		if ts.nextSeq != 6 {
			t.Errorf("%s published %d segments, want 5", ts.name, ts.nextSeq-1)
		}
	}
}

func TestOneRenditionPerAdaptationSet(t *testing.T) {
	origin := newLiveOrigin(t)
	origin.audios = []audioSet{
		{prefix: "a", lang: "zho", reps: []audioRep{
			{id: "a-low", bandwidth: 32000},
			{id: "a-high", bandwidth: 128000},
		}},
		{prefix: "a2", lang: "eng", reps: []audioRep{{id: "e0", bandwidth: 64000}}},
	}
	plan := planFor(t, origin)

	if len(plan.Audios) != 2 {
		t.Fatalf("planned %d audio tracks, want 2", len(plan.Audios))
	}
	if plan.Audios[0].ID != "a-high" {
		t.Errorf("took %s from the multi-bitrate set, want a-high", plan.Audios[0].ID)
	}
	if plan.Audios[1].ID != "e0" {
		t.Errorf("second language is %s, want e0", plan.Audios[1].ID)
	}
}

func TestAnUnsupportedAudioCodecOnlyCostsThatLanguage(t *testing.T) {
	origin := newLiveOrigin(t)
	origin.audios = []audioSet{
		{prefix: "a", lang: "zho", reps: []audioRep{{id: "a0", bandwidth: 32000}}},
		{prefix: "a2", lang: "eng", reps: []audioRep{{id: "e0", bandwidth: 192000, codecs: "ec-3"}}},
	}
	plan := planFor(t, origin)

	if !plan.Native() {
		t.Fatalf("engine = %s (%s), want the native path to survive one bad audio", plan.Engine, plan.Reason)
	}
	if len(plan.Audios) != 1 || plan.Audios[0].ID != "a0" {
		t.Fatalf("planned audios = %v, want just a0", plan.Audios)
	}

	if len(plan.SkippedAudios) != 1 || plan.SkippedAudios[0] != "e0" {
		t.Errorf("skipped = %v, want [e0]", plan.SkippedAudios)
	}
}

func planFor(t *testing.T, origin *liveOrigin) Plan {
	t.Helper()
	pres, err := mpd.Parse(origin.manifest(), "https://origin.example.com/live/stream.mpd")
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	plan, err := PlanFromManifest(pres, 0)
	if err != nil {
		t.Fatalf("PlanFromManifest: %v", err)
	}
	return plan
}
