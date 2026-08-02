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
	if strings.TrimSpace(sourceTimezone) == "" {
		sourceTimezone = DefaultTimezone
	}
	location, err := time.LoadLocation(sourceTimezone)
	if err != nil {
		return nil, fmt.Errorf("load XMLTV timezone %q: %w", sourceTimezone, err)
	}

	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "tv" {
			return nil, fmt.Errorf("decode XMLTV: root is %q, want tv", start.Name.Local)
		}
		return parseDocumentBytes(decoder, data, start, location)
	}
}

func parseDocumentBytes(decoder *xml.Decoder, data []byte, root xml.StartElement, location *time.Location) (*Document, error) {
	doc := &Document{
		Date:              xmlAttribute(root, "date"),
		SourceInfoURL:     xmlAttribute(root, "source-info-url"),
		SourceInfoName:    xmlAttribute(root, "source-info-name"),
		SourceDataURL:     xmlAttribute(root, "source-data-url"),
		GeneratorInfoName: xmlAttribute(root, "generator-info-name"),
		GeneratorInfoURL:  xmlAttribute(root, "generator-info-url"),
		Channels:          make([]Channel, 0),
		Programmes:        make([]Programme, 0),
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode XMLTV: %w", err)
		}
		switch item := token.(type) {
		case xml.StartElement:
			switch item.Name.Local {
			case "channel":
				channel, err := parseChannelBytes(decoder, data, item)
				if err != nil {
					return nil, fmt.Errorf("decode XMLTV channel %q: %w", xmlAttribute(item, "id"), err)
				}
				doc.Channels = append(doc.Channels, channel)
			case "programme":
				programme, err := parseProgrammeBytes(decoder, data, item, location)
				if err != nil {
					return nil, err
				}
				doc.Programmes = append(doc.Programmes, programme)
			default:
				if err := decoder.Skip(); err != nil {
					return nil, fmt.Errorf("decode XMLTV: %w", err)
				}
			}
		case xml.EndElement:
			if item.Name == root.Name {
				return doc, nil
			}
		}
	}
}

func parseChannelBytes(decoder *xml.Decoder, data []byte, element xml.StartElement) (Channel, error) {
	channel := Channel{
		ID: xmlAttribute(element, "id"), DisplayNames: make([]Text, 0), Icons: make([]Icon, 0),
	}
	innerStart := decoder.InputOffset()
	for {
		tokenOffset := decoder.InputOffset()
		token, err := decoder.Token()
		if err != nil {
			return Channel{}, err
		}
		switch child := token.(type) {
		case xml.StartElement:
			switch child.Name.Local {
			case "display-name":
				var value xmlText
				if err := decoder.DecodeElement(&value, &child); err != nil {
					return Channel{}, err
				}
				channel.DisplayNames = append(channel.DisplayNames, Text(value))
			case "icon":
				var value xmlIcon
				if err := decoder.DecodeElement(&value, &child); err != nil {
					return Channel{}, err
				}
				channel.Icons = append(channel.Icons, Icon(value))
			case "url":
				var value string
				if err := decoder.DecodeElement(&value, &child); err != nil {
					return Channel{}, err
				}
				channel.URLs = append(channel.URLs, value)
			default:
				if err := decoder.Skip(); err != nil {
					return Channel{}, err
				}
			}
		case xml.EndElement:
			if child.Name == element.Name {
				channel.InnerXML = string(data[innerStart:tokenOffset])
				return channel, nil
			}
		}
	}
}

func parseProgrammeBytes(decoder *xml.Decoder, data []byte, element xml.StartElement, location *time.Location) (Programme, error) {
	channelID := xmlAttribute(element, "channel")
	start, err := parseTimestamp(xmlAttribute(element, "start"), location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q start: %w", channelID, err)
	}
	stop, err := parseOptionalTimestamp(xmlAttribute(element, "stop"), location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q stop: %w", channelID, err)
	}
	pdcStart, err := parseOptionalTimestamp(xmlAttribute(element, "pdc-start"), location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q pdc-start: %w", channelID, err)
	}
	vpsStart, err := parseOptionalTimestamp(xmlAttribute(element, "vps-start"), location)
	if err != nil {
		return Programme{}, fmt.Errorf("decode XMLTV programme %q vps-start: %w", channelID, err)
	}
	programme := Programme{
		Start: start, Stop: stop, PDCStart: pdcStart, VPSStart: vpsStart,
		Channel: channelID, ShowView: xmlAttribute(element, "showview"),
		VideoPlus: xmlAttribute(element, "videoplus"), ClumpIndex: xmlAttribute(element, "clumpidx"),
		Titles: make([]Text, 0), SubTitles: make([]Text, 0),
		Descriptions: make([]Text, 0), Categories: make([]Text, 0),
	}
	innerStart := decoder.InputOffset()
	for {
		tokenOffset := decoder.InputOffset()
		token, err := decoder.Token()
		if err != nil {
			return Programme{}, fmt.Errorf("decode XMLTV programme %q body: %w", channelID, err)
		}
		switch child := token.(type) {
		case xml.StartElement:
			var target *[]Text
			switch child.Name.Local {
			case "title":
				target = &programme.Titles
			case "sub-title":
				target = &programme.SubTitles
			case "desc":
				target = &programme.Descriptions
			case "category":
				target = &programme.Categories
			}
			if target == nil {
				if err := decoder.Skip(); err != nil {
					return Programme{}, fmt.Errorf("decode XMLTV programme %q body: %w", channelID, err)
				}
				continue
			}
			var value xmlText
			if err := decoder.DecodeElement(&value, &child); err != nil {
				return Programme{}, fmt.Errorf("decode XMLTV programme %q body: %w", channelID, err)
			}
			*target = append(*target, Text(value))
		case xml.EndElement:
			if child.Name == element.Name {
				programme.InnerXML = string(data[innerStart:tokenOffset])
				return programme, nil
			}
		}
	}
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
		return Timestamp{Time: parsed, Offset: offset}, nil
	}
	parsed, err := time.ParseInLocation(layout, timestamp, location)
	if err != nil {
		return Timestamp{}, err
	}
	_, seconds := parsed.Zone()
	return Timestamp{Time: parsed, Offset: formatOffset(seconds)}, nil
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
