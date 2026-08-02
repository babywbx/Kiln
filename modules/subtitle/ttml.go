package subtitle

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const lineBreakMarker = '\ue000'

type ttmlPart struct {
	text  string
	child *ttmlNode
}

type ttmlNode struct {
	name  xml.Name
	attrs []xml.Attr
	parts []ttmlPart
}

type activeInterval struct {
	start  time.Duration
	end    time.Duration
	hasEnd bool
}

func ParseTTML(document []byte, options TTMLParseOptions) ([]Cue, error) {
	root, err := parseTTMLTree(document)
	if err != nil {
		return nil, err
	}
	if root.name.Local != "tt" {
		return nil, fmt.Errorf("TTML root is %q, want tt", root.name.Local)
	}

	timing, err := documentTiming(root, options.Timing)
	if err != nil {
		return nil, err
	}
	window := activeInterval{start: options.BaseTime}
	if options.DefaultDuration > 0 {
		window.end, err = addDuration(options.BaseTime, options.DefaultDuration)
		if err != nil {
			return nil, fmt.Errorf("TTML default window: %w", err)
		}
		window.hasEnd = true
	}

	var cues []Cue
	if err := collectCues(root, window, timing, false, &cues); err != nil {
		return nil, err
	}
	return cues, nil
}

func parseTTMLTree(document []byte) (*ttmlNode, error) {
	decoder := xml.NewDecoder(bytes.NewReader(document))
	decoder.Strict = true
	var root *ttmlNode
	var stack []*ttmlNode
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode TTML XML: %w", err)
		}
		switch token := token.(type) {
		case xml.StartElement:
			node := &ttmlNode{name: token.Name, attrs: token.Attr}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("TTML has multiple root elements")
				}
				root = node
			} else {
				parent := stack[len(stack)-1]
				parent.parts = append(parent.parts, ttmlPart{child: node})
			}
			stack = append(stack, node)
		case xml.CharData:
			if len(stack) != 0 {
				node := stack[len(stack)-1]
				node.parts = append(node.parts, ttmlPart{text: string(token)})
			}
		case xml.EndElement:
			if len(stack) == 0 {
				return nil, fmt.Errorf("unexpected TTML closing element %s", token.Name.Local)
			}
			stack = stack[:len(stack)-1]
		}
	}
	if root == nil {
		return nil, fmt.Errorf("empty TTML document")
	}
	return root, nil
}

func documentTiming(root *ttmlNode, fallback TimingParameters) (TimingParameters, error) {
	frameRateSpecified := fallback.FrameRate != 0
	tickRateSpecified := fallback.TickRate != 0
	timing := fallback.withDefaults()
	if value, ok := attribute(root, "frameRate"); ok {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed <= 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return TimingParameters{}, fmt.Errorf("invalid TTML frameRate %q", value)
		}
		timing.FrameRate = parsed
		frameRateSpecified = true
	}
	if value, ok := attribute(root, "frameRateMultiplier"); ok {
		fields := strings.Fields(value)
		if len(fields) != 2 {
			return TimingParameters{}, fmt.Errorf("invalid TTML frameRateMultiplier %q", value)
		}
		numerator, errNumerator := strconv.ParseFloat(fields[0], 64)
		denominator, errDenominator := strconv.ParseFloat(fields[1], 64)
		if errNumerator != nil || errDenominator != nil || numerator <= 0 || denominator <= 0 ||
			math.IsNaN(numerator) || math.IsNaN(denominator) || math.IsInf(numerator, 0) || math.IsInf(denominator, 0) {
			return TimingParameters{}, fmt.Errorf("invalid TTML frameRateMultiplier %q", value)
		}
		timing.FrameRate *= numerator / denominator
		if math.IsNaN(timing.FrameRate) || math.IsInf(timing.FrameRate, 0) {
			return TimingParameters{}, fmt.Errorf("invalid TTML frameRateMultiplier %q", value)
		}
	}
	if value, ok := attribute(root, "subFrameRate"); ok {
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil || parsed == 0 {
			return TimingParameters{}, fmt.Errorf("invalid TTML subFrameRate %q", value)
		}
		timing.SubFrameRate = uint32(parsed)
	}
	if value, ok := attribute(root, "tickRate"); ok {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || parsed <= 0 || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return TimingParameters{}, fmt.Errorf("invalid TTML tickRate %q", value)
		}
		timing.TickRate = parsed
		tickRateSpecified = true
	}
	if !tickRateSpecified {
		if frameRateSpecified {
			timing.TickRate = timing.FrameRate * float64(timing.SubFrameRate)
		} else {
			timing.TickRate = defaultTickRate
		}
	}
	return timing, nil
}

func collectCues(node *ttmlNode, parent activeInterval, timing TimingParameters, preserveSpace bool, cues *[]Cue) error {
	interval, err := nodeInterval(node, parent, timing)
	if err != nil {
		return fmt.Errorf("TTML %s timing: %w", node.name.Local, err)
	}
	if value, ok := attribute(node, "space"); ok {
		preserveSpace = value == "preserve"
	}

	if node.name.Local == "p" {
		if !interval.hasEnd {
			return fmt.Errorf("TTML cue has no end, duration, or enclosing time window")
		}
		if interval.end <= interval.start {
			return nil
		}
		text := extractCueText(node, preserveSpace)
		if text == "" {
			return nil
		}
		id, _ := attribute(node, "id")
		*cues = append(*cues, Cue{ID: id, Start: interval.start, End: interval.end, Text: text})
		return nil
	}

	for _, part := range node.parts {
		if part.child == nil {
			continue
		}
		if err := collectCues(part.child, interval, timing, preserveSpace, cues); err != nil {
			return err
		}
	}
	return nil
}

func nodeInterval(node *ttmlNode, parent activeInterval, timing TimingParameters) (activeInterval, error) {
	interval := activeInterval{start: parent.start, end: parent.end, hasEnd: parent.hasEnd}
	if expression, ok := attribute(node, "begin"); ok {
		begin, err := ParseTimeExpression(expression, timing)
		if err != nil {
			return activeInterval{}, err
		}
		interval.start, err = addDuration(parent.start, begin)
		if err != nil {
			return activeInterval{}, err
		}
	}

	var explicitEnd time.Duration
	hasExplicitEnd := false
	if expression, ok := attribute(node, "end"); ok {
		end, err := ParseTimeExpression(expression, timing)
		if err != nil {
			return activeInterval{}, err
		}
		explicitEnd, err = addDuration(parent.start, end)
		if err != nil {
			return activeInterval{}, err
		}
		hasExplicitEnd = true
	}
	if expression, ok := attribute(node, "dur"); ok {
		duration, err := ParseTimeExpression(expression, timing)
		if err != nil {
			return activeInterval{}, err
		}
		durationEnd, err := addDuration(interval.start, duration)
		if err != nil {
			return activeInterval{}, err
		}
		if !hasExplicitEnd || durationEnd < explicitEnd {
			explicitEnd = durationEnd
		}
		hasExplicitEnd = true
	}
	if hasExplicitEnd && (!interval.hasEnd || explicitEnd < interval.end) {
		interval.end = explicitEnd
		interval.hasEnd = true
	}
	return interval, nil
}

func addDuration(left, right time.Duration) (time.Duration, error) {
	if right > 0 && left > time.Duration(math.MaxInt64)-right {
		return 0, fmt.Errorf("time value overflows duration")
	}
	if right < 0 && left < time.Duration(math.MinInt64)-right {
		return 0, fmt.Errorf("time value underflows duration")
	}
	return left + right, nil
}

func attribute(node *ttmlNode, localName string) (string, bool) {
	for _, attr := range node.attrs {
		if attr.Name.Local == localName {
			return strings.TrimSpace(attr.Value), true
		}
	}
	return "", false
}

func extractCueText(node *ttmlNode, preserveSpace bool) string {
	var text strings.Builder
	appendNodeText(&text, node)
	raw := text.String()
	if preserveSpace {
		return strings.ReplaceAll(raw, string(lineBreakMarker), "\n")
	}
	return collapseCueWhitespace(raw)
}

func appendNodeText(text *strings.Builder, node *ttmlNode) {
	for _, part := range node.parts {
		if part.child == nil {
			text.WriteString(part.text)
			continue
		}
		if part.child.name.Local == "br" {
			text.WriteRune(lineBreakMarker)
			continue
		}
		appendNodeText(text, part.child)
	}
}

func collapseCueWhitespace(raw string) string {
	text := make([]rune, 0, len(raw))
	pendingSpace := false
	for _, character := range raw {
		switch {
		case character == lineBreakMarker:
			if len(text) != 0 && text[len(text)-1] == ' ' {
				text = text[:len(text)-1]
			}
			if len(text) != 0 && text[len(text)-1] != '\n' {
				text = append(text, '\n')
			}
			pendingSpace = false
		case unicode.IsSpace(character):
			pendingSpace = true
		default:
			if pendingSpace && len(text) != 0 && text[len(text)-1] != '\n' {
				text = append(text, ' ')
			}
			text = append(text, character)
			pendingSpace = false
		}
	}
	return strings.TrimSpace(string(text))
}
