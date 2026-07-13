package epg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const DefaultTimezone = "Asia/Hong_Kong"

var xmltvTimestampPattern = regexp.MustCompile(`^(\d{8}|\d{10}|\d{12}|\d{14})(?:\s*([+-]\d{4}))?$`)

type Text struct {
	Lang  string `json:"lang,omitempty"`
	Value string `json:"value"`
}

type Icon struct {
	Src    string `json:"src"`
	Width  string `json:"width,omitempty"`
	Height string `json:"height,omitempty"`
}

// Timestamp keeps the absolute instant and the source offset independently.
// Offset is always formatted as ±HHMM, including when it came from the source
// timezone fallback.
type Timestamp struct {
	Time   time.Time `json:"time"`
	Offset string    `json:"offset"`
}

func (t Timestamp) String() string {
	offset := t.Offset
	seconds, ok := parseOffset(offset)
	if !ok {
		_, seconds = t.Time.Zone()
		offset = formatOffset(seconds)
	}
	return t.Time.In(time.FixedZone("", seconds)).Format("20060102150405") + " " + offset
}

type Channel struct {
	ID           string   `json:"id"`
	DisplayNames []Text   `json:"display_names,omitempty"`
	Icons        []Icon   `json:"icons,omitempty"`
	URLs         []string `json:"urls,omitempty"`
	// InnerXML preserves source extensions. Clear it before Marshal when the
	// structured channel fields have been changed.
	InnerXML string `json:"-"`
}

type Programme struct {
	Start        Timestamp  `json:"start"`
	Stop         *Timestamp `json:"stop,omitempty"`
	PDCStart     *Timestamp `json:"pdc_start,omitempty"`
	VPSStart     *Timestamp `json:"vps_start,omitempty"`
	Channel      string     `json:"channel"`
	Titles       []Text     `json:"titles,omitempty"`
	SubTitles    []Text     `json:"sub_titles,omitempty"`
	Descriptions []Text     `json:"descriptions,omitempty"`
	Categories   []Text     `json:"categories,omitempty"`
	ShowView     string     `json:"showview,omitempty"`
	VideoPlus    string     `json:"videoplus,omitempty"`
	ClumpIndex   string     `json:"clump_index,omitempty"`
	// InnerXML preserves all programme children, including XMLTV extensions.
	InnerXML string `json:"-"`
}

type Document struct {
	Date              string      `json:"date,omitempty"`
	SourceInfoURL     string      `json:"source_info_url,omitempty"`
	SourceInfoName    string      `json:"source_info_name,omitempty"`
	SourceDataURL     string      `json:"source_data_url,omitempty"`
	GeneratorInfoName string      `json:"generator_info_name,omitempty"`
	GeneratorInfoURL  string      `json:"generator_info_url,omitempty"`
	Channels          []Channel   `json:"channels"`
	Programmes        []Programme `json:"programmes"`
}

type rawDocument struct {
	XMLName           xml.Name       `xml:"tv"`
	Date              string         `xml:"date,attr,omitempty"`
	SourceInfoURL     string         `xml:"source-info-url,attr,omitempty"`
	SourceInfoName    string         `xml:"source-info-name,attr,omitempty"`
	SourceDataURL     string         `xml:"source-data-url,attr,omitempty"`
	GeneratorInfoName string         `xml:"generator-info-name,attr,omitempty"`
	GeneratorInfoURL  string         `xml:"generator-info-url,attr,omitempty"`
	Channels          []rawChannel   `xml:"channel"`
	Programmes        []rawProgramme `xml:"programme"`
}

type rawChannel struct {
	ID       string `xml:"id,attr"`
	InnerXML string `xml:",innerxml"`
}

type rawProgramme struct {
	Start      string `xml:"start,attr"`
	Stop       string `xml:"stop,attr,omitempty"`
	PDCStart   string `xml:"pdc-start,attr,omitempty"`
	VPSStart   string `xml:"vps-start,attr,omitempty"`
	Channel    string `xml:"channel,attr"`
	ShowView   string `xml:"showview,attr,omitempty"`
	VideoPlus  string `xml:"videoplus,attr,omitempty"`
	ClumpIndex string `xml:"clumpidx,attr,omitempty"`
	InnerXML   string `xml:",innerxml"`
}

type channelChildren struct {
	DisplayNames []xmlText `xml:"display-name"`
	Icons        []xmlIcon `xml:"icon"`
	URLs         []string  `xml:"url"`
}

type programmeChildren struct {
	Titles       []xmlText `xml:"title"`
	SubTitles    []xmlText `xml:"sub-title"`
	Descriptions []xmlText `xml:"desc"`
	Categories   []xmlText `xml:"category"`
}

type xmlText struct {
	Lang  string `xml:"lang,attr,omitempty"`
	Value string `xml:",chardata"`
}

type xmlIcon struct {
	Src    string `xml:"src,attr"`
	Width  string `xml:"width,attr,omitempty"`
	Height string `xml:"height,attr,omitempty"`
}

// Parse decodes XMLTV using sourceTimezone when a timestamp omits its offset.
func Parse(r io.Reader, sourceTimezone string) (*Document, error) {
	if strings.TrimSpace(sourceTimezone) == "" {
		sourceTimezone = DefaultTimezone
	}
	location, err := time.LoadLocation(sourceTimezone)
	if err != nil {
		return nil, fmt.Errorf("load XMLTV timezone %q: %w", sourceTimezone, err)
	}

	var raw rawDocument
	decoder := xml.NewDecoder(r)
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode XMLTV: %w", err)
	}
	if raw.XMLName.Local != "tv" {
		return nil, fmt.Errorf("decode XMLTV: root is %q, want tv", raw.XMLName.Local)
	}

	doc := &Document{
		Date: raw.Date, SourceInfoURL: raw.SourceInfoURL,
		SourceInfoName: raw.SourceInfoName, SourceDataURL: raw.SourceDataURL,
		GeneratorInfoName: raw.GeneratorInfoName, GeneratorInfoURL: raw.GeneratorInfoURL,
		Channels:   make([]Channel, 0, len(raw.Channels)),
		Programmes: make([]Programme, 0, len(raw.Programmes)),
	}
	for _, item := range raw.Channels {
		children, err := parseChannelChildren(item.InnerXML)
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV channel %q: %w", item.ID, err)
		}
		doc.Channels = append(doc.Channels, Channel{
			ID: item.ID, DisplayNames: fromXMLTexts(children.DisplayNames),
			Icons: fromXMLIcons(children.Icons), URLs: children.URLs, InnerXML: item.InnerXML,
		})
	}
	for _, item := range raw.Programmes {
		start, err := parseTimestamp(item.Start, location)
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV programme %q start: %w", item.Channel, err)
		}
		var stop *Timestamp
		if item.Stop != "" {
			parsed, err := parseTimestamp(item.Stop, location)
			if err != nil {
				return nil, fmt.Errorf("decode XMLTV programme %q stop: %w", item.Channel, err)
			}
			stop = &parsed
		}
		pdcStart, err := parseOptionalTimestamp(item.PDCStart, location)
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV programme %q pdc-start: %w", item.Channel, err)
		}
		vpsStart, err := parseOptionalTimestamp(item.VPSStart, location)
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV programme %q vps-start: %w", item.Channel, err)
		}
		children, err := parseProgrammeChildren(item.InnerXML)
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV programme %q body: %w", item.Channel, err)
		}
		doc.Programmes = append(doc.Programmes, Programme{
			Start: start, Stop: stop, PDCStart: pdcStart, VPSStart: vpsStart, Channel: item.Channel,
			Titles: fromXMLTexts(children.Titles), SubTitles: fromXMLTexts(children.SubTitles),
			Descriptions: fromXMLTexts(children.Descriptions), Categories: fromXMLTexts(children.Categories),
			ShowView: item.ShowView, VideoPlus: item.VideoPlus,
			ClumpIndex: item.ClumpIndex, InnerXML: item.InnerXML,
		})
	}
	return doc, nil
}

func ParseBytes(data []byte, sourceTimezone string) (*Document, error) {
	return Parse(bytes.NewReader(data), sourceTimezone)
}

// Marshal encodes a complete XMLTV document with explicit timestamp offsets.
func Marshal(doc *Document) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("marshal XMLTV: nil document")
	}
	raw := rawDocument{
		Date: doc.Date, SourceInfoURL: doc.SourceInfoURL,
		SourceInfoName: doc.SourceInfoName, SourceDataURL: doc.SourceDataURL,
		GeneratorInfoName: doc.GeneratorInfoName, GeneratorInfoURL: doc.GeneratorInfoURL,
		Channels:   make([]rawChannel, 0, len(doc.Channels)),
		Programmes: make([]rawProgramme, 0, len(doc.Programmes)),
	}
	for _, item := range doc.Channels {
		body := item.InnerXML
		if body == "" {
			var err error
			body, err = marshalChannelChildren(item)
			if err != nil {
				return nil, fmt.Errorf("marshal XMLTV channel %q: %w", item.ID, err)
			}
		}
		raw.Channels = append(raw.Channels, rawChannel{ID: item.ID, InnerXML: body})
	}
	for _, item := range doc.Programmes {
		body := item.InnerXML
		if body == "" {
			var err error
			body, err = marshalProgrammeChildren(item)
			if err != nil {
				return nil, fmt.Errorf("marshal XMLTV programme %q: %w", item.Channel, err)
			}
		}
		entry := rawProgramme{
			Start: item.Start.String(), Channel: item.Channel,
			ShowView: item.ShowView, VideoPlus: item.VideoPlus,
			ClumpIndex: item.ClumpIndex, InnerXML: body,
		}
		if item.Stop != nil {
			entry.Stop = item.Stop.String()
		}
		if item.PDCStart != nil {
			entry.PDCStart = item.PDCStart.String()
		}
		if item.VPSStart != nil {
			entry.VPSStart = item.VPSStart.String()
		}
		raw.Programmes = append(raw.Programmes, entry)
	}
	payload, err := xml.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal XMLTV: %w", err)
	}
	return append([]byte(xml.Header), payload...), nil
}

func parseTimestamp(value string, location *time.Location) (Timestamp, error) {
	match := xmltvTimestampPattern.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q", value)
	}
	layouts := map[int]string{
		8: "20060102", 10: "2006010215", 12: "200601021504", 14: "20060102150405",
	}
	layout := layouts[len(match[1])]
	offset := match[2]
	if offset != "" {
		parsed, err := time.Parse(layout+" -0700", match[1]+" "+offset)
		if err != nil {
			return Timestamp{}, err
		}
		return Timestamp{Time: parsed, Offset: offset}, nil
	}
	parsed, err := time.ParseInLocation(layout, match[1], location)
	if err != nil {
		return Timestamp{}, err
	}
	_, seconds := parsed.Zone()
	return Timestamp{Time: parsed, Offset: formatOffset(seconds)}, nil
}

func parseOptionalTimestamp(value string, location *time.Location) (*Timestamp, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := parseTimestamp(value, location)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseOffset(value string) (int, bool) {
	if len(value) != 5 || (value[0] != '+' && value[0] != '-') {
		return 0, false
	}
	hours, err := strconv.Atoi(value[1:3])
	if err != nil {
		return 0, false
	}
	minutes, err := strconv.Atoi(value[3:5])
	if err != nil || hours > 23 || minutes > 59 {
		return 0, false
	}
	seconds := hours*3600 + minutes*60
	if value[0] == '-' {
		seconds = -seconds
	}
	return seconds, true
}

func formatOffset(seconds int) string {
	sign := '+'
	if seconds < 0 {
		sign = '-'
		seconds = -seconds
	}
	return fmt.Sprintf("%c%02d%02d", sign, seconds/3600, seconds%3600/60)
}

func parseChannelChildren(inner string) (channelChildren, error) {
	var children channelChildren
	err := xml.Unmarshal([]byte("<children>"+inner+"</children>"), &children)
	return children, err
}

func parseProgrammeChildren(inner string) (programmeChildren, error) {
	var children programmeChildren
	err := xml.Unmarshal([]byte("<children>"+inner+"</children>"), &children)
	return children, err
}

func marshalChannelChildren(item Channel) (string, error) {
	var buffer bytes.Buffer
	encoder := xml.NewEncoder(&buffer)
	for _, value := range item.DisplayNames {
		if err := encoder.Encode(xmlTextElement{xml.Name{Local: "display-name"}, value.Lang, value.Value}); err != nil {
			return "", err
		}
	}
	for _, value := range item.Icons {
		if err := encoder.Encode(xmlIconElement{xml.Name{Local: "icon"}, value.Src, value.Width, value.Height}); err != nil {
			return "", err
		}
	}
	for _, value := range item.URLs {
		if err := encoder.Encode(xmlStringElement{xml.Name{Local: "url"}, value}); err != nil {
			return "", err
		}
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

func marshalProgrammeChildren(item Programme) (string, error) {
	var buffer bytes.Buffer
	encoder := xml.NewEncoder(&buffer)
	groups := []struct {
		name   string
		values []Text
	}{
		{"title", item.Titles}, {"sub-title", item.SubTitles},
		{"desc", item.Descriptions}, {"category", item.Categories},
	}
	for _, group := range groups {
		for _, value := range group.values {
			if err := encoder.Encode(xmlTextElement{xml.Name{Local: group.name}, value.Lang, value.Value}); err != nil {
				return "", err
			}
		}
	}
	if err := encoder.Flush(); err != nil {
		return "", err
	}
	return buffer.String(), nil
}

type xmlTextElement struct {
	XMLName xml.Name
	Lang    string `xml:"lang,attr,omitempty"`
	Value   string `xml:",chardata"`
}

type xmlIconElement struct {
	XMLName xml.Name
	Src     string `xml:"src,attr"`
	Width   string `xml:"width,attr,omitempty"`
	Height  string `xml:"height,attr,omitempty"`
}

type xmlStringElement struct {
	XMLName xml.Name
	Value   string `xml:",chardata"`
}

func fromXMLTexts(values []xmlText) []Text {
	out := make([]Text, 0, len(values))
	for _, value := range values {
		out = append(out, Text(value))
	}
	return out
}

func fromXMLIcons(values []xmlIcon) []Icon {
	out := make([]Icon, 0, len(values))
	for _, value := range values {
		out = append(out, Icon(value))
	}
	return out
}
