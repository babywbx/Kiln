package subtitle

import "time"

// Cue is a subtitle cue on the absolute media timeline.
type Cue struct {
	ID    string
	Start time.Duration
	End   time.Duration
	Text  string
}

// Language contains the normalized BCP 47 tag and a user-facing track name.
type Language struct {
	Tag  string
	Name string
}

// STPPTrack provides the init-segment defaults required to read a fragmented
// ISO BMFF subtitle track.
type STPPTrack struct {
	ID                    uint32
	Timescale             uint32
	DefaultSampleDuration uint32
	DefaultSampleSize     uint32
	DefaultSampleFlags    uint32
}

// STPPSample is one decoded mdat sample with media-aligned timing.
type STPPSample struct {
	DecodeTime time.Duration
	Start      time.Duration
	End        time.Duration
	Duration   time.Duration
	Payload    []byte
}

// TTMLParseOptions anchors document-local TTML times to a media timeline.
type TTMLParseOptions struct {
	BaseTime        time.Duration
	DefaultDuration time.Duration
	Timing          TimingParameters
}

// WebVTTSegment is an independently publishable WebVTT media segment. Cue
// times remain absolute until serialization so clipping stays unambiguous.
type WebVTTSegment struct {
	Sequence uint64
	Language Language
	Start    time.Duration
	End      time.Duration
	MPEGTS   uint64
	Cues     []Cue
}
