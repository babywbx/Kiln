package mpd

import (
	"strings"
	"time"
)

type Presentation struct {
	Dynamic  bool
	Profiles string
	BaseURL  string

	Location string

	Refresh                    string
	MinimumUpdatePeriod        time.Duration
	AvailabilityStartTime      time.Time
	PublishTime                time.Time
	TimeShiftBufferDepth       time.Duration
	SuggestedPresentationDelay time.Duration
	MediaPresentationDuration  time.Duration
	MaxSegmentDuration         time.Duration
	UTCTimings                 []UTCTiming
	Periods                    []Period
}

type UTCTiming struct {
	Scheme string
	Value  string
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
	ID string

	PeriodID          string
	AdaptationSetID   string
	TrackKey          string
	Group             string
	Type              ContentType
	MimeType          string
	Codecs            string
	Bandwidth         int
	Width             int
	Height            int
	FrameRate         string
	Lang              string
	Roles             []string
	AudioChannels     int
	AudioSamplingRate string
	Trick             bool
	Essential         []string
	UnsupportedReason string

	DefaultKID string
	Encrypted  bool
	Scheme     string

	Addressing Addressing
}

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

type TimelineEntry struct {
	Time     uint64
	Duration uint64
	Repeat   int64
}

func (r Representation) IsVideo() bool { return r.Type == TypeVideo }
func (r Representation) IsAudio() bool { return r.Type == TypeAudio }
func (r Representation) IsText() bool  { return r.Type == TypeText }

type Segment struct {
	Number   uint64
	Time     uint64
	Duration uint64
	URL      string
}

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
