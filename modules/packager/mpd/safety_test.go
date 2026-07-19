//go:build extended

package mpd

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

func FuzzAvailableSegments(f *testing.F) {
	for _, seed := range []struct {
		timescale uint64
		duration  uint64
		repeat    int64
		start     uint64
		second    bool
	}{
		{1, 1, 3, 0, false},
		{1, 1, math.MaxInt64, 0, false},
		{1, math.MaxUint64, 1, 1, false},
		{1, 1, 49999, 0, true},
	} {
		f.Add(seed.timescale, seed.duration, seed.repeat, seed.start, seed.second)
	}
	f.Fuzz(func(t *testing.T, timescale, duration uint64, repeat int64, start uint64, second bool) {
		entries := []TimelineEntry{{Time: start, Duration: duration, Repeat: repeat}}
		if second {
			entries = append(entries, TimelineEntry{Time: start + duration, Duration: duration, Repeat: repeat})
		}
		rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateTimeline, Timescale: timescale, Media: "https://example.com/$Number$.m4s", Timeline: entries}}
		segments, err := testPresentation(rep).AvailableSegments(0, rep, time.Time{})
		if err == nil && len(segments) > 100000 {
			t.Fatalf("expanded %d segments", len(segments))
		}
	})
}

func FuzzAvailableLiveSegments(f *testing.F) {
	for _, seed := range []struct {
		timescale uint64
		duration  uint64
		repeat    int64
		start     uint64
		elapsed   int64
		depth     int64
	}{
		{1, 1, -1, 0, int64(200000 * time.Second), int64(30 * time.Second)},
		{1000, 2000, 999999, 0, int64(2000000 * time.Second), int64(30 * time.Second)},
		{1, 1, 100000, math.MaxUint64, int64(3 * time.Second), int64(time.Second)},
	} {
		f.Add(seed.timescale, seed.duration, seed.repeat, seed.start, seed.elapsed, seed.depth)
	}
	f.Fuzz(func(t *testing.T, timescale, duration uint64, repeat int64, start uint64, elapsed, depth int64) {
		rep := Representation{ID: "v", Addressing: Addressing{
			Mode:        AddressingTemplateTimeline,
			Timescale:   timescale,
			StartNumber: start,
			Timeline:    []TimelineEntry{{Duration: duration, Repeat: repeat}},
		}}
		p := testPresentation(rep)
		p.Dynamic = true
		p.AvailabilityStartTime = time.Unix(1, 0)
		p.TimeShiftBufferDepth = time.Duration(depth)
		segments, err := p.AvailableSegments(0, rep, p.AvailabilityStartTime.Add(time.Duration(elapsed)))
		if err == nil && len(segments) > maxTimelineExpansion {
			t.Fatalf("expanded %d segments", len(segments))
		}
	})
}

func TestAvailableSegmentsLongTimelineAllocatesForLiveWindow(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:        AddressingTemplateTimeline,
		Timescale:   1000,
		Media:       "https://example.com/$Number$.m4s",
		StartNumber: 10,
		Timeline: []TimelineEntry{{
			Duration: 2000,
			Repeat:   10*maxTimelineExpansion - 1,
		}},
	}}
	p := testPresentation(rep)
	p.Dynamic = true
	p.AvailabilityStartTime = time.Unix(1, 0)
	p.TimeShiftBufferDepth = 30 * time.Second
	now := p.AvailabilityStartTime.Add(2000000 * time.Second)

	assertWindow := func() {
		segments, err := p.AvailableSegments(0, rep, now)
		if err != nil {
			t.Fatalf("AvailableSegments: %v", err)
		}
		if len(segments) != 15 {
			t.Fatalf("segments=%d, want 15", len(segments))
		}
		if first := segments[0]; first.Number != 999995 || first.Time != 1999970000 {
			t.Fatalf("first segment=%+v", first)
		}
		if last := segments[len(segments)-1]; last.Number != 1000009 || last.Time != 1999998000 {
			t.Fatalf("last segment=%+v", last)
		}
	}

	assertWindow()
	if allocations := testing.AllocsPerRun(5, assertWindow); allocations > 64 {
		t.Fatalf("allocations=%0.0f, want at most 64 for the live window", allocations)
	}
}

func TestAvailableSegmentsLongOpenRepeatUsesLiveWindow(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:        AddressingTemplateTimeline,
		Timescale:   1,
		Media:       "https://example.com/$Number$.m4s",
		StartNumber: 1,
		Timeline:    []TimelineEntry{{Duration: 1, Repeat: -1}},
	}}
	p := testPresentation(rep)
	p.Dynamic = true
	p.AvailabilityStartTime = time.Unix(1, 0)
	p.TimeShiftBufferDepth = 30 * time.Second

	segments, err := p.AvailableSegments(0, rep, p.AvailabilityStartTime.Add(200000*time.Second))
	if err != nil {
		t.Fatalf("AvailableSegments: %v", err)
	}
	if len(segments) != 30 {
		t.Fatalf("segments=%d, want 30", len(segments))
	}
	if first := segments[0]; first.Number != 199971 || first.Time != 199970 {
		t.Fatalf("first segment=%+v", first)
	}
	if last := segments[len(segments)-1]; last.Number != 200000 || last.Time != 199999 {
		t.Fatalf("last segment=%+v", last)
	}
}

func TestAvailableSegmentsRejectsOversizedLiveWindow(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:      AddressingTemplateTimeline,
		Timescale: 1,
		Timeline:  []TimelineEntry{{Duration: 1, Repeat: -1}},
	}}
	p := testPresentation(rep)
	p.Dynamic = true
	p.AvailabilityStartTime = time.Unix(1, 0)
	p.TimeShiftBufferDepth = (maxTimelineExpansion + 1) * time.Second

	segments, err := p.AvailableSegments(0, rep, p.AvailabilityStartTime.Add(200001*time.Second))
	if !errors.Is(err, ErrExpansionLimit) || segments != nil {
		t.Fatalf("segments=%d err=%v", len(segments), err)
	}
}

func TestAvailableSegmentsRejectsNumberOverflowInSkippedHistory(t *testing.T) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:        AddressingTemplateTimeline,
		Timescale:   1,
		StartNumber: math.MaxUint64,
		Timeline:    []TimelineEntry{{Duration: 1, Repeat: 3}},
	}}
	p := testPresentation(rep)
	p.Dynamic = true
	p.AvailabilityStartTime = time.Unix(1, 0)
	p.TimeShiftBufferDepth = time.Second

	segments, err := p.AvailableSegments(0, rep, p.AvailabilityStartTime.Add(3*time.Second))
	if !errors.Is(err, ErrAddressingOverflow) || segments != nil {
		t.Fatalf("segments=%d err=%v", len(segments), err)
	}
}

func benchmarkTimeline(b *testing.B, count int) {
	rep := Representation{ID: "v", Addressing: Addressing{Mode: AddressingTemplateTimeline, Timescale: 1000, Media: "https://example.com/$Number$.m4s", Timeline: []TimelineEntry{{Duration: 2000, Repeat: int64(count - 1)}}}}
	p := testPresentation(rep)
	b.ReportAllocs()
	for b.Loop() {
		segments, err := p.AvailableSegments(0, rep, time.Time{})
		if err != nil || len(segments) != count {
			b.Fatalf("segments=%d err=%v", len(segments), err)
		}
	}
}

func BenchmarkTimeline(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) { benchmarkTimeline(b, count) })
	}
}

func BenchmarkTimelineLiveWindow(b *testing.B) {
	for _, count := range []int{1000, 100000, 1000000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			rep := Representation{ID: "v", Addressing: Addressing{
				Mode:      AddressingTemplateTimeline,
				Timescale: 1000,
				Media:     "https://example.com/$Number$.m4s",
				Timeline:  []TimelineEntry{{Duration: 2000, Repeat: int64(count - 1)}},
			}}
			p := testPresentation(rep)
			p.Dynamic = true
			p.AvailabilityStartTime = time.Unix(1, 0)
			p.TimeShiftBufferDepth = 30 * time.Second
			now := p.AvailabilityStartTime.Add(time.Duration(count*2) * time.Second)
			b.ReportAllocs()
			for b.Loop() {
				segments, err := p.AvailableSegments(0, rep, now)
				if err != nil || len(segments) != 15 {
					b.Fatalf("segments=%d err=%v", len(segments), err)
				}
			}
		})
	}
}

func BenchmarkTimelineShortLiveNoTrim(b *testing.B) {
	rep := Representation{ID: "v", Addressing: Addressing{
		Mode:      AddressingTemplateTimeline,
		Timescale: 1000,
		Media:     "https://example.com/$Number$.m4s",
		Timeline:  []TimelineEntry{{Duration: 2000, Repeat: 9}},
	}}
	p := testPresentation(rep)
	p.Dynamic = true
	p.AvailabilityStartTime = time.Unix(1, 0)
	p.TimeShiftBufferDepth = 30 * time.Second
	now := p.AvailabilityStartTime.Add(20 * time.Second)
	b.ReportAllocs()
	for b.Loop() {
		segments, err := p.AvailableSegments(0, rep, now)
		if err != nil || len(segments) != 10 {
			b.Fatalf("segments=%d err=%v", len(segments), err)
		}
	}
}
