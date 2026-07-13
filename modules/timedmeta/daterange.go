package timedmeta

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const dateRangeTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// DateRange is the deterministic subset of EXT-X-DATERANGE used for SCTE-35.
// An IN event is intentionally represented as its own observation; a stateful
// playlist publisher may merge it with an earlier OUT event sharing the ID.
type DateRange struct {
	ID              string
	Class           string
	StartDate       time.Time
	EndDate         *time.Time
	Duration        *time.Duration
	PlannedDuration *time.Duration
	SCTE35Out       string
	SCTE35In        string
	SCTE35Cmd       string
}

func (d DateRange) Equal(other DateRange) bool {
	return d.ID == other.ID &&
		d.Class == other.Class &&
		d.StartDate.Equal(other.StartDate) &&
		equalTimePointer(d.EndDate, other.EndDate) &&
		equalDurationPointer(d.Duration, other.Duration) &&
		equalDurationPointer(d.PlannedDuration, other.PlannedDuration) &&
		d.SCTE35Out == other.SCTE35Out &&
		d.SCTE35In == other.SCTE35In &&
		d.SCTE35Cmd == other.SCTE35Cmd
}

func equalTimePointer(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func equalDurationPointer(left, right *time.Duration) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// MergeDateRange combines observations for the same SCTE-35 event. OUT owns
// the start time; IN closes that range at its observation time.
func MergeDateRange(previous, observation DateRange) DateRange {
	if previous.ID == "" || previous.ID != observation.ID {
		return observation
	}
	merged := previous
	if merged.Class == "" {
		merged.Class = observation.Class
	}
	if merged.StartDate.IsZero() {
		merged.StartDate = observation.StartDate
	}
	if observation.SCTE35Out != "" {
		merged.SCTE35Out = observation.SCTE35Out
		merged.StartDate = observation.StartDate
		merged.PlannedDuration = observation.PlannedDuration
	}
	if observation.SCTE35In != "" {
		merged.SCTE35In = observation.SCTE35In
		if !observation.StartDate.IsZero() {
			end := observation.StartDate
			merged.EndDate = &end
			if !merged.StartDate.IsZero() && !end.Before(merged.StartDate) {
				duration := end.Sub(merged.StartDate)
				merged.Duration = &duration
			}
		}
	}
	if observation.SCTE35Cmd != "" {
		merged.SCTE35Cmd = observation.SCTE35Cmd
		if observation.Duration != nil {
			merged.Duration = observation.Duration
		}
	}
	return merged
}

func (e Event) DateRange(anchor ClockAnchor) (DateRange, bool, error) {
	start, err := e.wallClock(anchor)
	if err != nil {
		return DateRange{}, false, err
	}
	if e.Kind != KindSCTE35 || e.SCTE35 == nil {
		return DateRange{}, false, nil
	}

	dr := DateRange{
		ID:        fmt.Sprintf("scte35-%d", e.SCTE35.EventID),
		Class:     "com.apple.hls.scte35",
		StartDate: start,
	}
	command := "0x" + strings.ToUpper(hex.EncodeToString(e.Payload))
	switch e.SCTE35.Direction {
	case DirectionOut:
		dr.SCTE35Out = command
		if e.SCTE35.BreakDuration90k != nil {
			duration, durationErr := ticksDuration(*e.SCTE35.BreakDuration90k, 90000)
			if durationErr != nil {
				return DateRange{}, false, durationErr
			}
			dr.PlannedDuration = &duration
		}
	case DirectionIn:
		dr.SCTE35In = command
	default:
		dr.SCTE35Cmd = command
		if e.Duration > 0 {
			duration, durationErr := ticksDuration(e.Duration, e.TimeScale)
			if durationErr != nil {
				return DateRange{}, false, durationErr
			}
			dr.Duration = &duration
		}
	}
	return dr, true, nil
}

func (e Event) wallClock(anchor ClockAnchor) (time.Time, error) {
	if e.TimeScale == 0 {
		return time.Time{}, errorsf("event timescale is zero")
	}
	if anchor.TimeScale == 0 {
		return time.Time{}, errorsf("clock anchor timescale is zero")
	}
	if anchor.WallClock.IsZero() {
		return time.Time{}, errorsf("clock anchor wall time is zero")
	}
	eventOffset, err := ticksDuration(e.PresentationTime, e.TimeScale)
	if err != nil {
		return time.Time{}, err
	}
	anchorOffset, err := ticksDuration(anchor.PresentationTime, anchor.TimeScale)
	if err != nil {
		return time.Time{}, err
	}
	return anchor.WallClock.Add(eventOffset - anchorOffset), nil
}

func ticksDuration(ticks uint64, scale uint32) (time.Duration, error) {
	if scale == 0 {
		return 0, errorsf("timescale is zero")
	}
	seconds := ticks / uint64(scale)
	if seconds > uint64(math.MaxInt64/int64(time.Second)) {
		return 0, errorsf("timestamp exceeds time.Duration")
	}
	remainder := ticks % uint64(scale)
	nanos := remainder * uint64(time.Second) / uint64(scale)
	return time.Duration(seconds)*time.Second + time.Duration(nanos), nil
}

func errorsf(message string) error { return fmt.Errorf("timedmeta: %s", message) }

func (d DateRange) MarshalTag() string {
	attrs := []string{
		`ID="` + escapeAttribute(d.ID) + `"`,
		`CLASS="` + escapeAttribute(d.Class) + `"`,
		`START-DATE="` + d.StartDate.Format(dateRangeTimeLayout) + `"`,
	}
	if d.EndDate != nil {
		attrs = append(attrs, `END-DATE="`+d.EndDate.Format(dateRangeTimeLayout)+`"`)
	}
	if d.Duration != nil {
		attrs = append(attrs, "DURATION="+formatDuration(*d.Duration))
	}
	if d.PlannedDuration != nil {
		attrs = append(attrs, "PLANNED-DURATION="+formatDuration(*d.PlannedDuration))
	}
	if d.SCTE35Out != "" {
		attrs = append(attrs, `SCTE35-OUT="`+escapeAttribute(d.SCTE35Out)+`"`)
	}
	if d.SCTE35In != "" {
		attrs = append(attrs, `SCTE35-IN="`+escapeAttribute(d.SCTE35In)+`"`)
	}
	if d.SCTE35Cmd != "" {
		attrs = append(attrs, `SCTE35-CMD="`+escapeAttribute(d.SCTE35Cmd)+`"`)
	}
	return "#EXT-X-DATERANGE:" + strings.Join(attrs, ",")
}

func formatDuration(duration time.Duration) string {
	return strconv.FormatFloat(duration.Seconds(), 'f', 6, 64)
}

func escapeAttribute(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, `\"`, "\r", "", "\n", "").Replace(value)
}
