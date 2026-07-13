package mpd

import (
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
