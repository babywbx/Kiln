package mpd

import (
	"strings"
	"time"
)

// Presentation is the normalized manifest. Every Representation it carries is
// self-contained: no caller ever has to walk back up for inherited attributes.
type Presentation struct {
	Dynamic                    bool
	Profiles                   string
	BaseURL                    string
	MinimumUpdatePeriod        time.Duration
	AvailabilityStartTime      time.Time
	PublishTime                time.Time
	TimeShiftBufferDepth       time.Duration
	SuggestedPresentationDelay time.Duration
	MediaPresentationDuration  time.Duration
	MaxSegmentDuration         time.Duration
	Periods                    []Period
}

type Period struct {
	ID              string
	Start           time.Duration
	Duration        time.Duration
	Representations []Representation
}

type ContentType string

const (
	TypeVideo ContentType = "video"
	TypeAudio ContentType = "audio"
	TypeText  ContentType = "text"
)

type Representation struct {
	ID            string
	Type          ContentType
	MimeType      string
	Codecs        string
	Bandwidth     int
	Width         int
	Height        int
	FrameRate     string
	Lang          string
	Roles         []string
	AudioChannels int
	Trick         bool

	// DefaultKID is advisory only. The authoritative KID is the one in the
	// init segment's tenc box; this is used for cross-checking, and manifests
	// are allowed to omit it entirely.
	DefaultKID string
	Encrypted  bool
	Scheme     string

	Addressing Addressing
}

// AddressingMode says how media segment URLs are derived.
type AddressingMode string

const (
	AddressingTemplateDuration AddressingMode = "template_duration"
	AddressingTemplateTimeline AddressingMode = "template_timeline"
	AddressingList             AddressingMode = "list"
	AddressingBase             AddressingMode = "base"
)

type Addressing struct {
	Mode                   AddressingMode
	InitURL                string
	Media                  string
	Timescale              uint64
	Duration               uint64
	StartNumber            uint64
	PresentationTimeOffset uint64
	Timeline               []TimelineEntry
	List                   []string
}

// TimelineEntry is one S element. Repeat is the raw @r: -1 means "keep going
// until the period or the live edge ends", which is required, not optional.
type TimelineEntry struct {
	Time     uint64
	Duration uint64
	Repeat   int64
}

func (r Representation) IsVideo() bool { return r.Type == TypeVideo }
func (r Representation) IsAudio() bool { return r.Type == TypeAudio }

// Segment is one addressable media segment.
type Segment struct {
	Number   uint64
	Time     uint64
	Duration uint64
	URL      string
}

// StartTime is the presentation time of the segment in seconds.
func (s Segment) StartTime(timescale uint64) float64 {
	if timescale == 0 {
		return 0
	}
	return float64(s.Time) / float64(timescale)
}

func (s Segment) Seconds(timescale uint64) float64 {
	if timescale == 0 {
		return 0
	}
	return float64(s.Duration) / float64(timescale)
}

func codecFamily(codecs string) string {
	c := strings.ToLower(strings.TrimSpace(codecs))
	if i := strings.Index(c, "."); i > 0 {
		return c[:i]
	}
	return c
}
