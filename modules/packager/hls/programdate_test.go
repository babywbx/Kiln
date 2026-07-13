package hls

import (
	"strings"
	"testing"
	"time"
)

func TestProgramDateTimeAnchorsTheFirstSegment(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	at := time.Date(2026, 3, 1, 12, 0, 0, 500*int(time.Millisecond), time.UTC)

	publish(t, p, "audio-main", 1, 2)
	if err := p.PublishSegment(Publication{Track: "video-main", Seq: 1, Duration: 2, At: at}, []byte("d")); err != nil {
		t.Fatalf("PublishSegment: %v", err)
	}

	pl, _ := p.Playlist("video-main.m3u8")
	want := "#EXT-X-PROGRAM-DATE-TIME:2026-03-01T12:00:00.500Z"
	if !strings.Contains(string(pl), want) {
		t.Fatalf("playlist has no %s:\n%s", want, pl)
	}
	if got := strings.Count(string(pl), "#EXT-X-PROGRAM-DATE-TIME"); got != 1 {
		t.Errorf("program-date-time count = %d, want 1: the rest is extrapolated from EXTINF", got)
	}
}

func TestProgramDateTimeIsRestatedAfterADiscontinuity(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	at := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	publish(t, p, "audio-main", 1, 2)
	if err := p.PublishSegment(Publication{Track: "video-main", Seq: 1, Duration: 2, At: at}, []byte("d")); err != nil {
		t.Fatalf("PublishSegment: %v", err)
	}
	jumped := Publication{
		Track: "video-main", Seq: 2, Duration: 2,
		At: at.Add(time.Hour), Discontinuity: true,
	}
	if err := p.PublishSegment(jumped, []byte("d")); err != nil {
		t.Fatalf("PublishSegment: %v", err)
	}

	pl := string(mustPlaylist(t, p, "video-main.m3u8"))
	if got := strings.Count(pl, "#EXT-X-PROGRAM-DATE-TIME"); got != 2 {
		t.Fatalf("program-date-time count = %d, want 2: a player cannot extrapolate across a timeline jump\n%s", got, pl)
	}
	if !strings.Contains(pl, "#EXT-X-PROGRAM-DATE-TIME:2026-03-01T13:00:00.000Z") {
		t.Errorf("the segment after the discontinuity does not restate its wall-clock time:\n%s", pl)
	}
}

func TestProgramDateTimeIsOmittedWhenUnknown(t *testing.T) {
	p, _, _ := newTestPublisher(t, 4, time.Minute)
	publish(t, p, "audio-main", 1, 2)
	publish(t, p, "video-main", 1, 2)

	pl := string(mustPlaylist(t, p, "video-main.m3u8"))
	if strings.Contains(pl, "#EXT-X-PROGRAM-DATE-TIME") {
		t.Fatalf("a static source has no wall clock, so none should be claimed:\n%s", pl)
	}
}

func mustPlaylist(t *testing.T, p *Publisher, name string) []byte {
	t.Helper()
	pl, ok := p.Playlist(name)
	if !ok {
		t.Fatalf("no playlist %s", name)
	}
	return pl
}
