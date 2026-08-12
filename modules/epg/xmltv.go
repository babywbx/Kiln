package epg

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const DefaultTimezone = "UTC"

type Text struct {
	Lang  string `json:"lang,omitempty"`
	Value string `json:"value"`
}

type Icon struct {
	Src    string `json:"src"`
	Width  string `json:"width,omitempty"`
	Height string `json:"height,omitempty"`
}

type Timestamp struct {
	Time   time.Time `json:"time"`
	Offset string    `json:"offset"`
	Raw    string    `json:"-"`
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
	InnerXML     string   `json:"-"`
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
	InnerXML     string     `json:"-"`
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

// Handler receives one element at a time so callers never hold the whole guide.
type Handler struct {
	Header    func(Document) error
	Channel   func(Channel) error
	Programme func(Programme) error
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

type xmlChannel struct {
	ID           string    `xml:"id,attr"`
	DisplayNames []xmlText `xml:"display-name"`
	Icons        []xmlIcon `xml:"icon"`
	URLs         []string  `xml:"url"`
	InnerXML     string    `xml:",innerxml"`
}

type xmlProgramme struct {
	Start        string    `xml:"start,attr"`
	Stop         string    `xml:"stop,attr"`
	PDCStart     string    `xml:"pdc-start,attr"`
	VPSStart     string    `xml:"vps-start,attr"`
	Channel      string    `xml:"channel,attr"`
	ShowView     string    `xml:"showview,attr"`
	VideoPlus    string    `xml:"videoplus,attr"`
	ClumpIndex   string    `xml:"clumpidx,attr"`
	Titles       []xmlText `xml:"title"`
	SubTitles    []xmlText `xml:"sub-title"`
	Descriptions []xmlText `xml:"desc"`
	Categories   []xmlText `xml:"category"`
	InnerXML     string    `xml:",innerxml"`
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

func loadSourceLocation(sourceTimezone string) (*time.Location, error) {
	if strings.TrimSpace(sourceTimezone) == "" {
		sourceTimezone = DefaultTimezone
	}
	location, err := time.LoadLocation(sourceTimezone)
	if err != nil {
		return nil, fmt.Errorf("load XMLTV timezone %q: %w", sourceTimezone, err)
	}
	return location, nil
}

func Scan(r io.Reader, sourceTimezone string, handler Handler) error {
	location, err := loadSourceLocation(sourceTimezone)
	if err != nil {
		return err
	}
	decoder := xml.NewDecoder(r)
	root, err := scanRoot(decoder)
	if err != nil {
		return err
	}
	if handler.Header != nil {
		if err := handler.Header(documentHeader(root)); err != nil {
			return err
		}
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode XMLTV: %w", err)
		}
		switch item := token.(type) {
		case xml.StartElement:
			switch item.Name.Local {
			case "channel":
				channel, err := decodeChannel(decoder, item)
				if err != nil {
					return fmt.Errorf("decode XMLTV channel %q: %w", xmlAttribute(item, "id"), err)
				}
				if handler.Channel != nil {
					if err := handler.Channel(channel); err != nil {
						return err
					}
				}
			case "programme":
				programme, err := decodeProgramme(decoder, item, location)
				if err != nil {
					return err
				}
				if handler.Programme != nil {
					if err := handler.Programme(programme); err != nil {
						return err
					}
				}
			default:
				if err := decoder.Skip(); err != nil {
					return fmt.Errorf("decode XMLTV: %w", err)
				}
			}
		case xml.EndElement:
			if item.Name == root.Name {
				return nil
			}
		}
	}
}

func scanRoot(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, fmt.Errorf("decode XMLTV: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "tv" {
			return xml.StartElement{}, fmt.Errorf("decode XMLTV: root is %q, want tv", start.Name.Local)
		}
		return start, nil
	}
}

func documentHeader(root xml.StartElement) Document {
	return Document{
		Date:              xmlAttribute(root, "date"),
		SourceInfoURL:     xmlAttribute(root, "source-info-url"),
		SourceInfoName:    xmlAttribute(root, "source-info-name"),
		SourceDataURL:     xmlAttribute(root, "source-data-url"),
		GeneratorInfoName: xmlAttribute(root, "generator-info-name"),
		GeneratorInfoURL:  xmlAttribute(root, "generator-info-url"),
	}
}

func decodeChannel(decoder *xml.Decoder, element xml.StartElement) (Channel, error) {
	var item xmlChannel
	if err := decoder.DecodeElement(&item, &element); err != nil {
		return Channel{}, err
	}
	return Channel{
		ID: item.ID, DisplayNames: fromXMLTexts(item.DisplayNames),
		Icons: fromXMLIcons(item.Icons), URLs: item.URLs, InnerXML: item.InnerXML,
	}, nil
}

func decodeProgramme(decoder *xml.Decoder, element xml.StartElement, location *time.Location) (Programme, error) {
	var item xmlProgramme
	if err := decoder.DecodeElement(&item, &element); err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q body: %w", xmlAttribute(element, "channel"), err)
	}
	start, err := parseTimestamp(item.Start, location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q start: %w", item.Channel, err)
	}
	stop, err := parseOptionalTimestamp(item.Stop, location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q stop: %w", item.Channel, err)
	}
	pdcStart, err := parseOptionalTimestamp(item.PDCStart, location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q pdc-start: %w", item.Channel, err)
	}
	vpsStart, err := parseOptionalTimestamp(item.VPSStart, location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q vps-start: %w", item.Channel, err)
	}
	return Programme{
		Start: start, Stop: stop, PDCStart: pdcStart, VPSStart: vpsStart, Channel: item.Channel,
		Titles: fromXMLTexts(item.Titles), SubTitles: fromXMLTexts(item.SubTitles),
		Descriptions: fromXMLTexts(item.Descriptions), Categories: fromXMLTexts(item.Categories),
		ShowView: item.ShowView, VideoPlus: item.VideoPlus,
		ClumpIndex: item.ClumpIndex, InnerXML: item.InnerXML,
	}, nil
}

func Parse(r io.Reader, sourceTimezone string) (*Document, error) {
	doc := &Document{Channels: make([]Channel, 0), Programmes: make([]Programme, 0)}
	err := Scan(r, sourceTimezone, Handler{
		Header: func(header Document) error {
			header.Channels, header.Programmes = doc.Channels, doc.Programmes
			*doc = header
			return nil
		},
		Channel: func(channel Channel) error {
			doc.Channels = append(doc.Channels, channel)
			return nil
		},
		Programme: func(programme Programme) error {
			doc.Programmes = append(doc.Programmes, programme)
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

func ParseBytes(data []byte, sourceTimezone string) (*Document, error) {
	return Parse(bytes.NewReader(data), sourceTimezone)
}

func xmlAttribute(element xml.StartElement, name string) string {
	for _, attribute := range element.Attr {
		if attribute.Name.Local == name {
			return attribute.Value
		}
	}
	return ""
}

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
	trimmed := strings.TrimSpace(value)
	precision := 0
	for precision < len(trimmed) && isASCIIDigit(trimmed[precision]) {
		precision++
	}
	layout, ok := xmltvTimestampLayout(precision)
	if !ok {
		return Timestamp{}, fmt.Errorf("invalid timestamp %q", value)
	}
	timestamp := trimmed[:precision]
	remainder := trimmed[precision:]
	for len(remainder) > 0 && isASCIIWhitespace(remainder[0]) {
		remainder = remainder[1:]
	}
	offset := ""
	if len(remainder) > 0 {
		if len(remainder) != 5 || (remainder[0] != '+' && remainder[0] != '-') ||
			!allASCIIDigits(remainder[1:]) {
			return Timestamp{}, fmt.Errorf("invalid timestamp %q", value)
		}
		offset = remainder
	}
	if offset != "" {
		parsed, err := time.Parse(layout+" -0700", timestamp+" "+offset)
		if err != nil {
			return Timestamp{}, err
		}
		return Timestamp{Time: parsed, Offset: offset, Raw: value}, nil
	}
	parsed, err := time.ParseInLocation(layout, timestamp, location)
	if err != nil {
		return Timestamp{}, err
	}
	_, seconds := parsed.Zone()
	return Timestamp{Time: parsed, Offset: formatOffset(seconds), Raw: value}, nil
}

func xmltvTimestampLayout(precision int) (string, bool) {
	switch precision {
	case 8:
		return "20060102", true
	case 10:
		return "2006010215", true
	case 12:
		return "200601021504", true
	case 14:
		return "20060102150405", true
	default:
		return "", false
	}
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func allASCIIDigits(value string) bool {
	for index := range len(value) {
		if !isASCIIDigit(value[index]) {
			return false
		}
	}
	return true
}

func isASCIIWhitespace(value byte) bool {
	switch value {
	case ' ', '\t', '\n', '\f', '\r':
		return true
	default:
		return false
	}
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
