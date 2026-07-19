package mpd

import (
	"errors"
	"fmt"
	"math"
	"time"
)

const maxTimelineExpansion = 100000

var ErrExpansionLimit = errors.New("segment expansion limit exceeded")
var ErrAddressingOverflow = errors.New("segment addressing overflow")

type expansionBudget struct{ remaining uint64 }

func (b *expansionBudget) take(count uint64) error {
	if count > b.remaining {
		return ErrExpansionLimit
	}
	b.remaining -= count
	return nil
}

func checkedAdd(a, b uint64) (uint64, error) {
	if b > math.MaxUint64-a {
		return 0, ErrAddressingOverflow
	}
	return a + b, nil
}

func checkedMul(a, b uint64) (uint64, error) {
	if a != 0 && b > math.MaxUint64/a {
		return 0, ErrAddressingOverflow
	}
	return a * b, nil
}

func addressingError(repID, operation string, err error) error {
	return fmt.Errorf("representation %s: %s: %w", repID, operation, err)
}

func (p *Presentation) AvailableSegments(periodIdx int, rep Representation, now time.Time) ([]Segment, error) {
	if periodIdx < 0 || periodIdx >= len(p.Periods) {
		return nil, fmt.Errorf("period index %d out of range", periodIdx)
	}
	period := p.Periods[periodIdx]
	addr := rep.Addressing
	if addr.Timescale == 0 {
		return nil, fmt.Errorf("representation %s has zero timescale", rep.ID)
	}
	edge, bounded, err := p.liveEdgeTicks(period, addr, now)
	if err != nil {
		return nil, addressingError(rep.ID, "presentationTimeOffset+ticks", err)
	}
	horizon := uint64(math.MaxUint64)
	if bounded {
		horizon = edge
	} else {
		end, endErr := p.staticEndTicks(period, addr)
		if endErr != nil {
			return nil, addressingError(rep.ID, "presentationTimeOffset+ticks", endErr)
		}
		if end > 0 {
			horizon = end
		}
	}
	budget := expansionBudget{remaining: maxTimelineExpansion}
	var segs []Segment
	var timelineCutoff uint64
	var hasTimelineCutoff bool
	switch addr.Mode {
	case AddressingTemplateTimeline:
		timelineCutoff, hasTimelineCutoff = timeShiftCutoff(edge, p.TimeShiftBufferDepth, addr.Timescale, bounded)
		segs, err = timelineSegments(rep, horizon, timelineCutoff, hasTimelineCutoff, &budget)
	case AddressingTemplateDuration:
		segs, err = durationSegments(rep, horizon, &budget)
	case AddressingList:
		segs, err = listSegments(rep, &budget)
	default:
		return nil, fmt.Errorf("addressing mode %s is not supported", addr.Mode)
	}
	if err != nil {
		return nil, err
	}
	if bounded {
		if addr.Mode == AddressingTemplateTimeline {
			if hasTimelineCutoff {
				segs = trimToCutoff(segs, timelineCutoff)
			}
		} else {
			segs = trimToTimeShift(segs, edge, p.TimeShiftBufferDepth, addr.Timescale)
		}
	}
	return segs, nil
}

func (p *Presentation) liveEdgeTicks(period Period, addr Addressing, now time.Time) (uint64, bool, error) {
	if !p.Dynamic || p.AvailabilityStartTime.IsZero() {
		return 0, false, nil
	}
	elapsed := now.Sub(p.AvailabilityStartTime) - period.Start
	if elapsed <= 0 {
		return addr.PresentationTimeOffset, true, nil
	}
	ticks := float64(elapsed) / float64(time.Second) * float64(addr.Timescale)
	if ticks < 0 || ticks > math.MaxUint64 {
		return 0, true, ErrAddressingOverflow
	}
	edge, err := checkedAdd(uint64(ticks), addr.PresentationTimeOffset)
	return edge, true, err
}

func (p *Presentation) staticEndTicks(period Period, addr Addressing) (uint64, error) {
	d := period.Duration
	if d == 0 {
		d = p.MediaPresentationDuration
	}
	if d <= 0 {
		return 0, nil
	}
	ticks := float64(d) / float64(time.Second) * float64(addr.Timescale)
	if ticks > math.MaxUint64 {
		return 0, ErrAddressingOverflow
	}
	return checkedAdd(uint64(ticks), addr.PresentationTimeOffset)
}

func timeShiftCutoff(edge uint64, depth time.Duration, timescale uint64, bounded bool) (uint64, bool) {
	if !bounded || depth <= 0 {
		return 0, false
	}
	depthTicks := uint64(float64(depth) / float64(time.Second) * float64(timescale))
	if depthTicks >= edge {
		return 0, false
	}
	return edge - depthTicks, true
}

func timelineSegments(rep Representation, horizon, cutoff uint64, hasCutoff bool, budget *expansionBudget) ([]Segment, error) {
	addr := rep.Addressing
	var out []Segment
	number := addr.StartNumber
	for entryIndex, e := range addr.Timeline {
		if e.Repeat >= 0 {
			if e.Repeat == math.MaxInt64 {
				return nil, addressingError(rep.ID, "repeat+1", ErrAddressingOverflow)
			}
			count, err := checkedAdd(uint64(e.Repeat), 1)
			if err != nil {
				return nil, addressingError(rep.ID, "repeat+1", err)
			}
			startIndex := uint64(0)
			expansionCount := count
			if hasCutoff && e.Duration > 0 {
				startIndex = expiredTimelineSegments(e, count, cutoff)
				publishedCount := publishedTimelineSegments(e, count, horizon)
				expansionCount = publishedCount - startIndex
			}
			if err = budget.take(expansionCount); err != nil {
				return nil, addressingError(rep.ID, "timeline expansion", err)
			}
			if hasCutoff && out == nil && expansionCount > 0 {
				out = make([]Segment, 0, int(expansionCount))
			}
			if startIndex > 0 {
				hasFollowing := startIndex < count || entryIndex+1 < len(addr.Timeline)
				number, err = advanceSegmentNumber(number, startIndex, hasFollowing)
				if err != nil {
					return nil, addressingError(rep.ID, "startNumber+index", err)
				}
			}
			for i := startIndex; i < count; i++ {
				offset, mulErr := checkedMul(i, e.Duration)
				if mulErr != nil {
					return nil, addressingError(rep.ID, "index*duration", mulErr)
				}
				t, addErr := checkedAdd(e.Time, offset)
				if addErr != nil {
					return nil, addressingError(rep.ID, "time+index*duration", addErr)
				}
				end, endErr := checkedAdd(t, e.Duration)
				if endErr != nil {
					return nil, addressingError(rep.ID, "time+duration", endErr)
				}
				if end > horizon {
					return out, nil
				}
				out = append(out, makeSegment(rep, number, t, e.Duration))
				number, addErr = checkedAdd(number, 1)
				if addErr != nil && (i+1 < count || entryIndex+1 < len(addr.Timeline)) {
					return nil, addressingError(rep.ID, "startNumber+index", addErr)
				}
			}
			continue
		}
		if horizon == math.MaxUint64 {
			return nil, fmt.Errorf("representation %s: @r=-1 without a live edge or period duration", rep.ID)
		}
		startIndex := uint64(0)
		if hasCutoff && e.Duration > 0 {
			startIndex = expiredTimelineSegments(e, math.MaxUint64, cutoff)
			publishedCount := publishedTimelineSegments(e, math.MaxUint64, horizon)
			expansionCount := publishedCount - startIndex
			if err := budget.take(expansionCount); err != nil {
				return nil, addressingError(rep.ID, "timeline expansion", err)
			}
			if out == nil && expansionCount > 0 {
				out = make([]Segment, 0, int(expansionCount))
			}
			var err error
			number, err = advanceSegmentNumber(number, startIndex, true)
			if err != nil {
				return nil, addressingError(rep.ID, "startNumber+index", err)
			}
		}
		for i := startIndex; ; i++ {
			offset, err := checkedMul(i, e.Duration)
			if err != nil {
				return nil, addressingError(rep.ID, "index*duration", err)
			}
			t, err := checkedAdd(e.Time, offset)
			if err != nil {
				return nil, addressingError(rep.ID, "time+index*duration", err)
			}
			end, err := checkedAdd(t, e.Duration)
			if err != nil {
				return nil, addressingError(rep.ID, "time+duration", err)
			}
			if end > horizon {
				break
			}
			if !hasCutoff || e.Duration == 0 {
				if err = budget.take(1); err != nil {
					return nil, addressingError(rep.ID, "timeline expansion", err)
				}
			}
			out = append(out, makeSegment(rep, number, t, e.Duration))
			number, err = checkedAdd(number, 1)
			if err != nil {
				return nil, addressingError(rep.ID, "startNumber+index", err)
			}
		}
	}
	return out, nil
}

func expiredTimelineSegments(entry TimelineEntry, count, cutoff uint64) uint64 {
	if cutoff <= entry.Time {
		return 0
	}
	expired := (cutoff - entry.Time) / entry.Duration
	if expired > count {
		return count
	}
	return expired
}

func publishedTimelineSegments(entry TimelineEntry, count, horizon uint64) uint64 {
	if horizon <= entry.Time {
		return 0
	}
	published := (horizon - entry.Time) / entry.Duration
	if published > count {
		return count
	}
	return published
}

func advanceSegmentNumber(number, count uint64, hasFollowing bool) (uint64, error) {
	advanced, err := checkedAdd(number, count)
	if err == nil {
		return advanced, nil
	}
	// A finite timeline never uses the increment after its final segment.
	if !hasFollowing && number > 0 && count == math.MaxUint64-number+1 {
		return 0, nil
	}
	return 0, err
}

func durationSegments(rep Representation, horizon uint64, budget *expansionBudget) ([]Segment, error) {
	addr := rep.Addressing
	if addr.Duration == 0 {
		return nil, fmt.Errorf("representation %s: template without @duration", rep.ID)
	}
	if horizon == math.MaxUint64 {
		return nil, fmt.Errorf("representation %s: @duration addressing without a live edge or period duration", rep.ID)
	}
	if horizon <= addr.PresentationTimeOffset {
		return nil, nil
	}
	count := (horizon - addr.PresentationTimeOffset) / addr.Duration
	if err := budget.take(count); err != nil {
		return nil, addressingError(rep.ID, "duration expansion", err)
	}
	out := make([]Segment, 0, count)
	for i := uint64(0); i < count; i++ {
		offset, err := checkedMul(i, addr.Duration)
		if err != nil {
			return nil, addressingError(rep.ID, "index*duration", err)
		}
		t, err := checkedAdd(addr.PresentationTimeOffset, offset)
		if err != nil {
			return nil, addressingError(rep.ID, "presentationTimeOffset+ticks", err)
		}
		number, err := checkedAdd(addr.StartNumber, i)
		if err != nil {
			return nil, addressingError(rep.ID, "startNumber+index", err)
		}
		out = append(out, makeSegment(rep, number, t, addr.Duration))
	}
	return out, nil
}

func listSegments(rep Representation, budget *expansionBudget) ([]Segment, error) {
	addr := rep.Addressing
	if err := budget.take(uint64(len(addr.List))); err != nil {
		return nil, addressingError(rep.ID, "list expansion", err)
	}
	out := make([]Segment, 0, len(addr.List))
	for i, u := range addr.List {
		index := uint64(i)
		number, err := checkedAdd(addr.StartNumber, index)
		if err != nil {
			return nil, addressingError(rep.ID, "startNumber+index", err)
		}
		offset, err := checkedMul(index, addr.Duration)
		if err != nil {
			return nil, addressingError(rep.ID, "index*duration", err)
		}
		segmentTime, err := checkedAdd(addr.PresentationTimeOffset, offset)
		if err != nil {
			return nil, addressingError(rep.ID, "presentationTimeOffset+ticks", err)
		}
		out = append(out, Segment{Number: number, Time: segmentTime, Duration: addr.Duration, URL: u})
	}
	return out, nil
}

func makeSegment(rep Representation, number, t, dur uint64) Segment {
	addr := rep.Addressing
	return Segment{Number: number, Time: t, Duration: dur, URL: expandIdentifiers(addr.Media, rep.ID, rep.Bandwidth, number, t)}
}

func trimToTimeShift(segs []Segment, edge uint64, depth time.Duration, timescale uint64) []Segment {
	if len(segs) == 0 {
		return segs
	}
	cutoff, ok := timeShiftCutoff(edge, depth, timescale, true)
	if !ok {
		return segs
	}
	return trimToCutoff(segs, cutoff)
}

func trimToCutoff(segs []Segment, cutoff uint64) []Segment {
	for i, s := range segs {
		end, err := checkedAdd(s.Time, s.Duration)
		if err != nil || end > cutoff {
			return segs[i:]
		}
	}
	return nil
}
