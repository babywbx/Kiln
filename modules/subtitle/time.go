package subtitle

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultFrameRate    = 30
	defaultSubFrameRate = 1
	defaultTickRate     = 1
)

var (
	clockTimePattern  = regexp.MustCompile(`^([0-9]{2,}):([0-9]{2}):([0-9]{2})(?:\.([0-9]+)|:([0-9]{2})(?:\.([0-9]+))?)?$`)
	offsetTimePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(h|m|s|ms|f|t)$`)
)

// TimingParameters contains the TTML timing rates used by frame and tick
// expressions. Zero fields use the TTML defaults.
type TimingParameters struct {
	FrameRate    float64
	SubFrameRate uint32
	TickRate     float64
}

func (p TimingParameters) withDefaults() TimingParameters {
	if p.FrameRate == 0 {
		p.FrameRate = defaultFrameRate
	}
	if p.SubFrameRate == 0 {
		p.SubFrameRate = defaultSubFrameRate
	}
	if p.TickRate == 0 {
		p.TickRate = defaultTickRate
	}
	return p
}

// ParseTimeExpression parses the clock-time and offset-time forms commonly
// used by TTML subtitle streams.
func ParseTimeExpression(expression string, timing TimingParameters) (time.Duration, error) {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		return 0, fmt.Errorf("empty TTML time expression")
	}
	timing = timing.withDefaults()
	if timing.FrameRate <= 0 || math.IsNaN(timing.FrameRate) || math.IsInf(timing.FrameRate, 0) {
		return 0, fmt.Errorf("invalid frame rate %v", timing.FrameRate)
	}
	if timing.TickRate <= 0 || math.IsNaN(timing.TickRate) || math.IsInf(timing.TickRate, 0) {
		return 0, fmt.Errorf("invalid tick rate %v", timing.TickRate)
	}

	if parts := clockTimePattern.FindStringSubmatch(expression); parts != nil {
		return parseClockTime(expression, parts, timing)
	}
	parts := offsetTimePattern.FindStringSubmatch(expression)
	if parts == nil {
		return 0, fmt.Errorf("invalid TTML time expression %q", expression)
	}
	value, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, fmt.Errorf("parse TTML time expression %q: %w", expression, err)
	}
	var seconds float64
	switch parts[2] {
	case "h":
		seconds = value * 60 * 60
	case "m":
		seconds = value * 60
	case "s":
		seconds = value
	case "ms":
		seconds = value / 1000
	case "f":
		seconds = value / timing.FrameRate
	case "t":
		seconds = value / timing.TickRate
	}
	return secondsToDuration(expression, seconds)
}

func parseClockTime(expression string, parts []string, timing TimingParameters) (time.Duration, error) {
	hours, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid TTML clock hours in %q", expression)
	}
	minutes, err := strconv.ParseUint(parts[2], 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid TTML clock minutes in %q", expression)
	}
	seconds, err := strconv.ParseUint(parts[3], 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid TTML clock seconds in %q", expression)
	}
	if minutes >= 60 || seconds >= 60 {
		return 0, fmt.Errorf("invalid TTML clock time %q", expression)
	}

	total := float64(hours)*60*60 + float64(minutes)*60 + float64(seconds)
	if fraction := parts[4]; fraction != "" {
		value, err := strconv.ParseFloat("0."+fraction, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid TTML clock fraction in %q", expression)
		}
		total += value
	}
	if frames := parts[5]; frames != "" {
		frame, err := strconv.ParseUint(frames, 10, 32)
		if err != nil {
			return 0, fmt.Errorf("invalid TTML clock frame in %q", expression)
		}
		frameValue := float64(frame)
		if subframes := parts[6]; subframes != "" {
			subframe, err := strconv.ParseUint(subframes, 10, 32)
			if err != nil {
				return 0, fmt.Errorf("invalid TTML clock subframe in %q", expression)
			}
			if subframe >= uint64(timing.SubFrameRate) {
				return 0, fmt.Errorf("invalid TTML subframe in %q", expression)
			}
			frameValue += float64(subframe) / float64(timing.SubFrameRate)
		}
		if frameValue >= timing.FrameRate {
			return 0, fmt.Errorf("invalid TTML frame in %q", expression)
		}
		total += frameValue / timing.FrameRate
	}
	return secondsToDuration(expression, total)
}

func secondsToDuration(expression string, seconds float64) (time.Duration, error) {
	nanoseconds := seconds * float64(time.Second)
	if math.IsNaN(nanoseconds) || math.IsInf(nanoseconds, 0) || nanoseconds > math.MaxInt64 || nanoseconds < math.MinInt64 {
		return 0, fmt.Errorf("TTML time expression %q overflows duration", expression)
	}
	return time.Duration(math.Round(nanoseconds)), nil
}
