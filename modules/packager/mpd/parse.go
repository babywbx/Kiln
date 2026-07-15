// Package mpd is a structured DASH manifest model. It replaces regex trimming
// so the native path can reason about inheritance, timelines and live edges.
package mpd

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type xmlMPD struct {
	XMLName                   xml.Name        `xml:"MPD"`
	Type                      string          `xml:"type,attr"`
	Profiles                  string          `xml:"profiles,attr"`
	MinimumUpdatePeriod       string          `xml:"minimumUpdatePeriod,attr"`
	AvailabilityStartTime     string          `xml:"availabilityStartTime,attr"`
	TimeShiftBufferDepth      string          `xml:"timeShiftBufferDepth,attr"`
	SuggestedPresentationDlay string          `xml:"suggestedPresentationDelay,attr"`
	PublishTime               string          `xml:"publishTime,attr"`
	MediaPresentationDuration string          `xml:"mediaPresentationDuration,attr"`
	MaxSegmentDuration        string          `xml:"maxSegmentDuration,attr"`
	MinBufferTime             string          `xml:"minBufferTime,attr"`
	BaseURL                   []string        `xml:"BaseURL"`
	Location                  []string        `xml:"Location"`
	UTCTimings                []xmlDescriptor `xml:"UTCTiming"`
	Periods                   []xmlPeriod     `xml:"Period"`
}

type xmlPeriod struct {
	ID              string             `xml:"id,attr"`
	Start           string             `xml:"start,attr"`
	Duration        string             `xml:"duration,attr"`
	BaseURL         []string           `xml:"BaseURL"`
	SegmentTemplate *xmlSegmentTmpl    `xml:"SegmentTemplate"`
	AdaptationSets  []xmlAdaptationSet `xml:"AdaptationSet"`
}

type xmlAdaptationSet struct {
	ID                string           `xml:"id,attr"`
	ContentType       string           `xml:"contentType,attr"`
	MimeType          string           `xml:"mimeType,attr"`
	Codecs            string           `xml:"codecs,attr"`
	Lang              string           `xml:"lang,attr"`
	Width             int              `xml:"width,attr"`
	Height            int              `xml:"height,attr"`
	FrameRate         string           `xml:"frameRate,attr"`
	AudioSamplingRate string           `xml:"audioSamplingRate,attr"`
	MaxPlayoutRate    string           `xml:"maxPlayoutRate,attr"`
	BaseURL           []string         `xml:"BaseURL"`
	Essential         []xmlDescriptor  `xml:"EssentialProperty"`
	Roles             []xmlDescriptor  `xml:"Role"`
	ContentProtection []xmlProtection  `xml:"ContentProtection"`
	AudioChannelConf  []xmlDescriptor  `xml:"AudioChannelConfiguration"`
	SegmentTemplate   *xmlSegmentTmpl  `xml:"SegmentTemplate"`
	SegmentList       *xmlSegmentList  `xml:"SegmentList"`
	Representations   []xmlRepresation `xml:"Representation"`
}

type xmlRepresation struct {
	ID                string          `xml:"id,attr"`
	MimeType          string          `xml:"mimeType,attr"`
	Codecs            string          `xml:"codecs,attr"`
	Bandwidth         int             `xml:"bandwidth,attr"`
	Width             int             `xml:"width,attr"`
	Height            int             `xml:"height,attr"`
	FrameRate         string          `xml:"frameRate,attr"`
	AudioSamplingRate string          `xml:"audioSamplingRate,attr"`
	MaxPlayoutRate    string          `xml:"maxPlayoutRate,attr"`
	BaseURL           []string        `xml:"BaseURL"`
	Essential         []xmlDescriptor `xml:"EssentialProperty"`
	Roles             []xmlDescriptor `xml:"Role"`
	ContentProtection []xmlProtection `xml:"ContentProtection"`
	AudioChannelConf  []xmlDescriptor `xml:"AudioChannelConfiguration"`
	SegmentTemplate   *xmlSegmentTmpl `xml:"SegmentTemplate"`
	SegmentList       *xmlSegmentList `xml:"SegmentList"`
	SegmentBase       *xmlSegmentBase `xml:"SegmentBase"`
}

type xmlDescriptor struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
}

type xmlProtection struct {
	SchemeIDURI string `xml:"schemeIdUri,attr"`
	Value       string `xml:"value,attr"`
	DefaultKID  string `xml:"default_KID,attr"`
	PSSH        string `xml:"pssh"`
}

type xmlSegmentTmpl struct {
	Initialization         string       `xml:"initialization,attr"`
	Media                  string       `xml:"media,attr"`
	Timescale              uint64       `xml:"timescale,attr"`
	Duration               uint64       `xml:"duration,attr"`
	StartNumber            *uint64      `xml:"startNumber,attr"`
	PresentationTimeOffset uint64       `xml:"presentationTimeOffset,attr"`
	SegmentTimeline        *xmlTimeline `xml:"SegmentTimeline"`
}

type xmlTimeline struct {
	S []xmlS `xml:"S"`
}

type xmlS struct {
	T *uint64 `xml:"t,attr"`
	D uint64  `xml:"d,attr"`
	R *int64  `xml:"r,attr"`
	N *uint64 `xml:"n,attr"`
}

type xmlSegmentList struct {
	Timescale              uint64          `xml:"timescale,attr"`
	Duration               uint64          `xml:"duration,attr"`
	PresentationTimeOffset uint64          `xml:"presentationTimeOffset,attr"`
	StartNumber            *uint64         `xml:"startNumber,attr"`
	Initialization         *xmlInit        `xml:"Initialization"`
	SegmentURLs            []xmlSegmentURL `xml:"SegmentURL"`
}

type xmlInit struct {
	SourceURL string `xml:"sourceURL,attr"`
	Range     string `xml:"range,attr"`
}

type xmlSegmentURL struct {
	Media      string `xml:"media,attr"`
	MediaRange string `xml:"mediaRange,attr"`
}

type xmlSegmentBase struct {
	Initialization *xmlInit `xml:"Initialization"`
	IndexRange     string   `xml:"indexRange,attr"`
}

// Parse decodes a manifest. baseURL must be the final URL the manifest was
// fetched from, after redirects, so relative references resolve correctly.
func Parse(data []byte, baseURL string) (*Presentation, error) {
	return parse(data, baseURL, false)
}

// ParseForInspection decodes enough of a manifest to describe every track,
// including tracks whose addressing is not supported by the native engine.
// Playback continues to use Parse so unsupported manifests still fail closed.
func ParseForInspection(data []byte, baseURL string) (*Presentation, error) {
	return parse(data, baseURL, true)
}

func parse(data []byte, baseURL string, tolerant bool) (*Presentation, error) {
	var doc xmlMPD
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	dec.Entity = nil
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("parse mpd: %w", err)
	}
	if doc.XMLName.Local != "MPD" {
		return nil, fmt.Errorf("parse mpd: root element is %s", doc.XMLName.Local)
	}

	root, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse mpd base url: %w", err)
	}

	p := &Presentation{
		Dynamic:  strings.EqualFold(doc.Type, "dynamic"),
		BaseURL:  baseURL,
		Profiles: doc.Profiles,
	}
	for _, timing := range doc.UTCTimings {
		if strings.TrimSpace(timing.SchemeIDURI) == "" || strings.TrimSpace(timing.Value) == "" {
			continue
		}
		p.UTCTimings = append(p.UTCTimings, UTCTiming{Scheme: strings.TrimSpace(timing.SchemeIDURI), Value: strings.TrimSpace(timing.Value)})
	}
	for _, loc := range doc.Location {
		loc = strings.TrimSpace(loc)
		if loc == "" {
			continue
		}
		if abs, err := resolveRef(root, loc); err == nil {
			p.Location = abs
		}
		break
	}
	if p.MinimumUpdatePeriod, err = optDuration(doc.MinimumUpdatePeriod); err != nil {
		return nil, err
	}
	if p.TimeShiftBufferDepth, err = optDuration(doc.TimeShiftBufferDepth); err != nil {
		return nil, err
	}
	if p.SuggestedPresentationDelay, err = optDuration(doc.SuggestedPresentationDlay); err != nil {
		return nil, err
	}
	if p.MediaPresentationDuration, err = optDuration(doc.MediaPresentationDuration); err != nil {
		return nil, err
	}
	if p.MaxSegmentDuration, err = optDuration(doc.MaxSegmentDuration); err != nil {
		return nil, err
	}
	if p.AvailabilityStartTime, err = optTime(doc.AvailabilityStartTime); err != nil {
		return nil, err
	}
	if p.PublishTime, err = optTime(doc.PublishTime); err != nil {
		return nil, err
	}
	if p.Dynamic && p.AvailabilityStartTime.IsZero() {
		return nil, fmt.Errorf("parse mpd: dynamic manifest without availabilityStartTime")
	}

	mpdBase := resolveAll(root, doc.BaseURL)
	for _, xp := range doc.Periods {
		period, err := normalizePeriod(xp, mpdBase, tolerant)
		if err != nil {
			return nil, err
		}
		p.Periods = append(p.Periods, period)
	}
	if len(p.Periods) == 0 {
		return nil, fmt.Errorf("parse mpd: no period")
	}
	return p, nil
}

// resolveAll folds a chain of BaseURL elements onto a parent URL. Only the
// first BaseURL of each level is used; multi-origin selection is not in scope.
func resolveAll(parent *url.URL, bases []string) *url.URL {
	out := parent
	for _, b := range bases {
		b = strings.TrimSpace(b)
		if b == "" {
			continue
		}
		ref, err := url.Parse(b)
		if err != nil {
			continue
		}
		out = out.ResolveReference(ref)
		break
	}
	return out
}

func resolveRef(base *url.URL, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("empty url reference")
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("bad url reference %q: %w", ref, err)
	}
	return base.ResolveReference(u).String(), nil
}

func optDuration(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	d, err := ParseDuration(s)
	if err != nil {
		return 0, err
	}
	return d, nil
}

func optTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("bad xs:dateTime %q", s)
}

// ParseDuration reads an xs:duration such as PT1H2M3.5S. Years and months are
// rejected: they have no fixed length and no DASH manifest needs them here.
func ParseDuration(s string) (time.Duration, error) {
	in := strings.TrimSpace(s)
	neg := strings.HasPrefix(in, "-")
	in = strings.TrimPrefix(in, "-")
	if !strings.HasPrefix(in, "P") {
		return 0, fmt.Errorf("bad xs:duration %q", s)
	}
	in = in[1:]
	datePart, timePart, hasTime := strings.Cut(in, "T")
	if hasTime && timePart == "" {
		return 0, fmt.Errorf("bad xs:duration %q", s)
	}
	if datePart == "" && !hasTime {
		return 0, fmt.Errorf("bad xs:duration %q", s)
	}

	var total time.Duration
	accum := func(part string, units map[byte]time.Duration) error {
		num := strings.Builder{}
		for i := 0; i < len(part); i++ {
			c := part[i]
			if (c >= '0' && c <= '9') || c == '.' {
				num.WriteByte(c)
				continue
			}
			unit, ok := units[c]
			if !ok {
				return fmt.Errorf("bad xs:duration %q", s)
			}
			if num.Len() == 0 {
				return fmt.Errorf("bad xs:duration %q", s)
			}
			v, err := strconv.ParseFloat(num.String(), 64)
			if err != nil {
				return fmt.Errorf("bad xs:duration %q", s)
			}
			total += time.Duration(v * float64(unit))
			num.Reset()
		}
		if num.Len() > 0 {
			return fmt.Errorf("bad xs:duration %q", s)
		}
		return nil
	}

	if err := accum(datePart, map[byte]time.Duration{
		'D': 24 * time.Hour,
		'W': 7 * 24 * time.Hour,
	}); err != nil {
		return 0, err
	}
	if hasTime {
		if err := accum(timePart, map[byte]time.Duration{
			'H': time.Hour,
			'M': time.Minute,
			'S': time.Second,
		}); err != nil {
			return 0, err
		}
	}
	if neg {
		total = -total
	}
	return total, nil
}
