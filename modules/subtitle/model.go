package subtitle

import "time"

type Cue struct {
	ID    string
	Start time.Duration
	End   time.Duration
	Text  string
}

type Language struct {
	Tag  string
	Name string
}

type STPPTrack struct {
	ID                    uint32
	Timescale             uint32
	DefaultSampleDuration uint32
	DefaultSampleSize     uint32
	DefaultSampleFlags    uint32
}

type STPPSample struct {
	DecodeTime time.Duration
	Start      time.Duration
	End        time.Duration
	Duration   time.Duration
	Payload    []byte
}

type TTMLParseOptions struct {
	BaseTime        time.Duration
	DefaultDuration time.Duration
	Timing          TimingParameters
}

type WebVTTSegment struct {
	Sequence uint64
	Language Language
	Start    time.Duration
	End      time.Duration
	MPEGTS   uint64
	Cues     []Cue
}
