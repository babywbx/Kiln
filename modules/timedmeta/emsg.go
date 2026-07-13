package timedmeta

import (
	"fmt"
	"math"
	"strings"
)

// Emsg contains the already-decoded fields of a DASH Event Message box.
// SegmentPresentationTime is the earliest presentation time of the containing
// segment, in TimeScale units, and is required to resolve a version 0 delta.
type Emsg struct {
	Version                 uint8
	TimeScale               uint32
	PresentationTimeDelta   uint32
	PresentationTime        uint64
	EventDuration           uint32
	ID                      uint32
	SchemeIDURI             string
	Value                   string
	MessageData             []byte
	SegmentPresentationTime uint64
}

func FromEmsg(in Emsg) (Event, error) {
	if in.TimeScale == 0 {
		return Event{}, fmt.Errorf("timedmeta: emsg timescale is zero")
	}

	presentationTime := in.PresentationTime
	switch in.Version {
	case 0:
		if uint64(in.PresentationTimeDelta) > math.MaxUint64-in.SegmentPresentationTime {
			return Event{}, fmt.Errorf("timedmeta: emsg v0 presentation time overflows")
		}
		presentationTime = in.SegmentPresentationTime + uint64(in.PresentationTimeDelta)
	case 1:
	default:
		return Event{}, fmt.Errorf("timedmeta: unsupported emsg version %d", in.Version)
	}

	event := Event{
		ID:               in.ID,
		Kind:             classifyScheme(in.SchemeIDURI),
		SchemeIDURI:      in.SchemeIDURI,
		Value:            in.Value,
		PresentationTime: presentationTime,
		TimeScale:        in.TimeScale,
		Duration:         uint64(in.EventDuration),
		Payload:          append([]byte(nil), in.MessageData...),
	}
	if event.Kind == KindSCTE35 {
		parsed, err := ParseSCTE35(event.Payload)
		if err != nil {
			return Event{}, fmt.Errorf("timedmeta: parse SCTE-35 emsg: %w", err)
		}
		event.SCTE35 = &parsed
	}
	return event, nil
}

func classifyScheme(scheme string) Kind {
	normalized := strings.ToLower(strings.TrimSpace(scheme))
	if strings.HasPrefix(normalized, "urn:scte:scte35:") && strings.Contains(normalized, ":bin") {
		return KindSCTE35
	}
	switch normalized {
	case "https://aomedia.org/emsg/id3", "urn:aomedia:emsg:id3":
		return KindID3
	default:
		return KindEmsg
	}
}
