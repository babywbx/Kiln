package packager

import (
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/packager/mpd"
)

func TestProgramDateTimeSubtractsThePresentationTimeOffset(t *testing.T) {
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n := &Native{epoch: epoch}
	ts := &trackState{rep: mpd.Representation{Addressing: mpd.Addressing{
		Timescale:              90000,
		PresentationTimeOffset: 900000,
	}}}

	at := n.segmentTime(ts, mpd.Segment{Time: 1080000})

	want := epoch.Add(2 * time.Second)
	if !at.Equal(want) {
		t.Fatalf("segment time = %s, want %s: t=1080000 sits 2s past a PTO of 900000 at 90kHz", at, want)
	}
}

func TestProgramDateTimeIsWithheldBeforeThePresentationStarts(t *testing.T) {
	n := &Native{epoch: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ts := &trackState{rep: mpd.Representation{Addressing: mpd.Addressing{
		Timescale:              90000,
		PresentationTimeOffset: 900000,
	}}}

	if at := n.segmentTime(ts, mpd.Segment{Time: 450000}); !at.IsZero() {
		t.Fatalf("segment time = %s, want none: a segment before the offset has no wall clock we can defend", at)
	}
}

func TestProgramDateTimeFollowsTheManifestTimeline(t *testing.T) {
	origin := newLiveOrigin(t)
	n, _ := startLive(t, origin, newClock())

	pl := videoPlaylist(t, n)
	want := "#EXT-X-PROGRAM-DATE-TIME:2026-01-01T00:00:06.000Z"
	if !strings.Contains(pl, want) {
		t.Fatalf("want %s: availabilityStartTime is 00:00:00Z and the first segment sits at t=6000/1000\n%s", want, pl)
	}
}

func TestProgramDateTimeIsAbsentFromAStaticSource(t *testing.T) {
	job, _ := runNative(t, "hevc")

	pl, ok := job.Publication().Playlist("video-main.m3u8")
	if !ok {
		t.Fatal("no video playlist")
	}
	if strings.Contains(string(pl), "#EXT-X-PROGRAM-DATE-TIME") {
		t.Fatalf("a static presentation has no availability clock to anchor to:\n%s", pl)
	}
}
