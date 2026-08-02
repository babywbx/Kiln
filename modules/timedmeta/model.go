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

type ClockAnchor struct {
	WallClock        time.Time
	PresentationTime uint64
	TimeScale        uint32
}
