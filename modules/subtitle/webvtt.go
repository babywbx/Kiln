package subtitle

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

const mpegTSTimestampMask = (1 << 33) - 1

var webVTTEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

func ClipCues(cues []Cue, start, end time.Duration) []Cue {
	if end <= start {
		return nil
	}
	clipped := make([]Cue, 0, len(cues))
	for _, cue := range cues {
		if cue.End <= start || cue.Start >= end || cue.End <= cue.Start {
			continue
		}
		if cue.Start < start {
			cue.Start = start
		}
		if cue.End > end {
			cue.End = end
		}
		clipped = append(clipped, cue)
	}
	return clipped
}

func NewWebVTTSegment(cues []Cue, start, end time.Duration, mpegTS uint64) (WebVTTSegment, error) {
	if end <= start {
		return WebVTTSegment{}, fmt.Errorf("invalid WebVTT segment window [%v, %v)", start, end)
	}
	return WebVTTSegment{
		Start:  start,
		End:    end,
		MPEGTS: mpegTS & mpegTSTimestampMask,
		Cues:   ClipCues(cues, start, end),
	}, nil
}

func MarshalWebVTT(segment WebVTTSegment) ([]byte, error) {
	if segment.End <= segment.Start {
		return nil, fmt.Errorf("invalid WebVTT segment window [%v, %v)", segment.Start, segment.End)
	}

	var output bytes.Buffer
	output.WriteString("WEBVTT\n")
	fmt.Fprintf(&output, "X-TIMESTAMP-MAP=LOCAL:00:00:00.000,MPEGTS:%d\n\n", segment.MPEGTS)
	for _, cue := range ClipCues(segment.Cues, segment.Start, segment.End) {
		if validCueID(cue.ID) {
			output.WriteString(cue.ID)
			output.WriteByte('\n')
		}
		fmt.Fprintf(
			&output,
			"%s --> %s\n",
			formatWebVTTTime(cue.Start-segment.Start),
			formatWebVTTTime(cue.End-segment.Start),
		)
		text := strings.ReplaceAll(cue.Text, "\r\n", "\n")
		text = strings.ReplaceAll(text, "\r", "\n")
		output.WriteString(webVTTEscaper.Replace(text))
		output.WriteString("\n\n")
	}
	return output.Bytes(), nil
}

func validCueID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "\r\n") && !strings.Contains(id, "-->")
}

func formatWebVTTTime(value time.Duration) string {
	if value < 0 {
		value = 0
	}
	totalMilliseconds := value.Round(time.Millisecond).Milliseconds()
	hours := totalMilliseconds / 3_600_000
	totalMilliseconds %= 3_600_000
	minutes := totalMilliseconds / 60_000
	totalMilliseconds %= 60_000
	seconds := totalMilliseconds / 1_000
	milliseconds := totalMilliseconds % 1_000
	return fmt.Sprintf("%02d:%02d:%02d.%03d", hours, minutes, seconds, milliseconds)
}
