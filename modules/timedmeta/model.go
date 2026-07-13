// Package timedmeta maps timed metadata from DASH into a transport-neutral
// event model and the attributes needed by HLS playlists.
package timedmeta

import "time"

type Kind string

const (
	KindEmsg   Kind = "emsg"
	KindSCTE35 Kind = "scte35"
	KindID3    Kind = "id3"
)

type Direction string

const (
	DirectionUnknown Direction = "unknown"
	DirectionOut     Direction = "out"
	DirectionIn      Direction = "in"
)

// Event is independent of MP4 and HLS. PresentationTime and Duration are
// expressed in TimeScale ticks; callers must provide an explicit clock anchor
// before converting them to wall-clock time.
type Event struct {
	ID               uint32
	Kind             Kind
	SchemeIDURI      string
	Value            string
	PresentationTime uint64
	TimeScale        uint32
	Duration         uint64
	Payload          []byte
	SCTE35           *SCTE35
}

// ClockAnchor states which presentation timestamp corresponds to a wall-clock
// instant. Its timescale may differ from the event timescale.
type ClockAnchor struct {
	WallClock        time.Time
	PresentationTime uint64
	TimeScale        uint32
}
