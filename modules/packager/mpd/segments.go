package mpd

import (
	"fmt"
	"math"
	"time"
)

// maxTimelineExpansion bounds an @r=-1 run so a bogus availabilityStartTime
// cannot make us allocate forever.
const maxTimelineExpansion = 100000

// AvailableSegments returns the segments of rep that the origin should already
// be serving at wall-clock time now, trimmed to the time-shift buffer.
//
// A segment counts as available once its last sample is published, i.e. once
// availabilityStartTime + periodStart + (t + d - pto)/timescale <= now.
func (p *Presentation) AvailableSegments(periodIdx int, rep Representation, now time.Time) ([]Segment, error) {
	if periodIdx < 0 || periodIdx >= len(p.Periods) {
		return nil, fmt.Errorf("period index %d out of range", periodIdx)
	}
	period := p.Periods[periodIdx]
	addr := rep.Addressing
	if addr.Timescale == 0 {
		return nil, fmt.Errorf("representation %s has zero timescale", rep.ID)
	}

	edge, bounded := p.liveEdgeTicks(period, addr, now)
	horizon := uint64(math.MaxUint64)
	if bounded {
		horizon = edge
	} else if d := p.staticEndTicks(period, addr); d > 0 {
		horizon = d
	}

	var segs []Segment
	var err error
	switch addr.Mode {
	case AddressingTemplateTimeline:
		segs, err = timelineSegments(rep, horizon)
	case AddressingTemplateDuration:
		segs, err = durationSegments(rep, horizon)
	case AddressingList:
		segs, err = listSegments(rep)
	default:
		return nil, fmt.Errorf("addressing mode %s is not supported", addr.Mode)
	}
	if err != nil {
		return nil, err
	}
	if bounded {
		segs = trimToTimeShift(segs, edge, p.TimeShiftBufferDepth, addr.Timescale)
	}
	return segs, nil
}

// liveEdgeTicks converts now into this representation's media timeline. The
// second result is false for static manifests, which have no live edge.
func (p *Presentation) liveEdgeTicks(period Period, addr Addressing, now time.Time) (uint64, bool) {
	if !p.Dynamic || p.AvailabilityStartTime.IsZero() {
		return 0, false
	}
	elapsed := now.Sub(p.AvailabilityStartTime) - period.Start
	if elapsed <= 0 {
		return addr.PresentationTimeOffset, true
	}
	ticks := float64(elapsed) / float64(time.Second) * float64(addr.Timescale)
	if ticks < 0 || ticks > math.MaxUint64 {
		return addr.PresentationTimeOffset, true
	}
	return uint64(ticks) + addr.PresentationTimeOffset, true
}

func (p *Presentation) staticEndTicks(period Period, addr Addressing) uint64 {
	d := period.Duration
	if d == 0 {
		d = p.MediaPresentationDuration
	}
	if d <= 0 {
		return 0
	}
	return uint64(float64(d)/float64(time.Second)*float64(addr.Timescale)) + addr.PresentationTimeOffset
}

// timelineSegments expands SegmentTimeline, including @r=-1, which repeats
// until the live edge (or the period end for static manifests).
func timelineSegments(rep Representation, horizon uint64) ([]Segment, error) {
	addr := rep.Addressing
	var out []Segment
	number := addr.StartNumber
	for _, e := range addr.Timeline {
		if e.Repeat >= 0 {
			for i := int64(0); i <= e.Repeat; i++ {
				t := e.Time + uint64(i)*e.Duration
				if t+e.Duration > horizon {
					return out, nil
				}
				out = append(out, makeSegment(rep, number, t, e.Duration))
				number++
			}
			continue
		}
		if horizon == math.MaxUint64 {
			return nil, fmt.Errorf("representation %s: @r=-1 without a live edge or period duration", rep.ID)
		}
		for i := 0; ; i++ {
			if i > maxTimelineExpansion {
				return nil, fmt.Errorf("representation %s: @r=-1 expanded past %d segments", rep.ID, maxTimelineExpansion)
			}
			t := e.Time + uint64(i)*e.Duration
			if t+e.Duration > horizon {
				break
			}
			out = append(out, makeSegment(rep, number, t, e.Duration))
			number++
		}
	}
	return out, nil
}

func durationSegments(rep Representation, horizon uint64) ([]Segment, error) {
	addr := rep.Addressing
	if addr.Duration == 0 {
		return nil, fmt.Errorf("representation %s: template without @duration", rep.ID)
	}
	if horizon == math.MaxUint64 {
		return nil, fmt.Errorf("representation %s: @duration addressing without a live edge or period duration", rep.ID)
	}
	start := addr.PresentationTimeOffset
	if horizon <= start {
		return nil, nil
	}
	count := (horizon - start) / addr.Duration
	if count > maxTimelineExpansion {
		return nil, fmt.Errorf("representation %s: %d segments exceeds the expansion cap", rep.ID, count)
	}
	out := make([]Segment, 0, count)
	for i := range count {
		t := start + i*addr.Duration
		out = append(out, makeSegment(rep, addr.StartNumber+i, t, addr.Duration))
	}
	return out, nil
}

func listSegments(rep Representation) ([]Segment, error) {
	addr := rep.Addressing
	out := make([]Segment, 0, len(addr.List))
	for i, u := range addr.List {
		out = append(out, Segment{
			Number:   addr.StartNumber + uint64(i),
			Time:     addr.PresentationTimeOffset + uint64(i)*addr.Duration,
			Duration: addr.Duration,
			URL:      u,
		})
	}
	return out, nil
}

func makeSegment(rep Representation, number, t, dur uint64) Segment {
	addr := rep.Addressing
	return Segment{
		Number:   number,
		Time:     t,
		Duration: dur,
		URL:      expandIdentifiers(addr.Media, rep.ID, rep.Bandwidth, number, t),
	}
}

// trimToTimeShift drops segments that have already fallen out of the origin's
// time-shift buffer; requesting them yields 404/410.
func trimToTimeShift(segs []Segment, edge uint64, depth time.Duration, timescale uint64) []Segment {
	if depth <= 0 || len(segs) == 0 {
		return segs
	}
	depthTicks := uint64(float64(depth) / float64(time.Second) * float64(timescale))
	if depthTicks >= edge {
		return segs
	}
	cutoff := edge - depthTicks
	for i, s := range segs {
		if s.Time+s.Duration > cutoff {
			return segs[i:]
		}
	}
	return nil
}
