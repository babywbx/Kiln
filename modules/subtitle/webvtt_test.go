package subtitle

import (
	"reflect"
	"testing"
	"time"
)

func TestClipCuesClipsCrossingCuesToHalfOpenWindow(t *testing.T) {
	t.Parallel()

	cues := []Cue{
		{ID: "before", Start: time.Second, End: 3 * time.Second, Text: "before"},
		{ID: "a", Start: time.Second, End: 5 * time.Second, Text: "A & <B>"},
		{ID: "b", Start: 4 * time.Second, End: 7 * time.Second, Text: "line 1\nline 2"},
		{ID: "after", Start: 6 * time.Second, End: 7 * time.Second, Text: "after"},
	}
	got := ClipCues(cues, 3*time.Second, 6*time.Second)
	want := []Cue{
		{ID: "a", Start: 3 * time.Second, End: 5 * time.Second, Text: "A & <B>"},
		{ID: "b", Start: 4 * time.Second, End: 6 * time.Second, Text: "line 1\nline 2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ClipCues = %#v, want %#v", got, want)
	}
}

func TestMarshalWebVTTSegmentRebasesTimesAndEscapesCueText(t *testing.T) {
	t.Parallel()

	segment, err := NewWebVTTSegment([]Cue{
		{ID: "a", Start: time.Second, End: 5 * time.Second, Text: "A & <B>"},
		{ID: "b", Start: 4 * time.Second, End: 7 * time.Second, Text: "line 1\nline 2"},
	}, 3*time.Second, 6*time.Second, 270000)
	if err != nil {
		t.Fatalf("NewWebVTTSegment: %v", err)
	}
	got, err := MarshalWebVTT(segment)
	if err != nil {
		t.Fatalf("MarshalWebVTT: %v", err)
	}
	want := "WEBVTT\n" +
		"X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:270000\n\n" +
		"a\n00:00:00.000 --> 00:00:02.000\nA &amp; &lt;B&gt;\n\n" +
		"b\n00:00:01.000 --> 00:00:03.000\nline 1\nline 2\n\n"
	if string(got) != want {
		t.Fatalf("MarshalWebVTT output:\n%s\nwant:\n%s", got, want)
	}
}

func TestNewWebVTTSegmentRejectsInvalidWindow(t *testing.T) {
	t.Parallel()

	if _, err := NewWebVTTSegment(nil, 5*time.Second, 5*time.Second, 0); err == nil {
		t.Fatal("NewWebVTTSegment unexpectedly accepted an empty window")
	}
}

func TestNewWebVTTSegmentWrapsMPEGTSTo33Bits(t *testing.T) {
	t.Parallel()

	segment, err := NewWebVTTSegment(nil, 0, time.Second, (1<<33)+90)
	if err != nil {
		t.Fatalf("NewWebVTTSegment: %v", err)
	}
	if segment.MPEGTS != 90 {
		t.Fatalf("segment MPEGTS = %d, want 90", segment.MPEGTS)
	}
}
