package hls

import (
	"fmt"
	"strconv"
	"strings"
)

const MasterName = "master.m3u8"

const programDateLayout = "2006-01-02T15:04:05.000Z07:00"

func mediaPlaylist(t *track, endList bool) []byte {
	return mediaPlaylistWithOptions(t, endList, PlaylistOptions{})
}

func mediaPlaylistWithOptions(t *track, endList bool, options PlaylistOptions) []byte {
	b := make([]byte, 0, 128+len(t.segments)*96)
	lowLatency := t.hasParts()
	skipped := 0
	if lowLatency && !endList && options.Skip {
		skipped = deltaSkipCount(t)
	}
	b = appendMediaHeader(b, t, lowLatency || skipped > 0, endList)
	if skipped > 0 {
		b = append(b, "#EXT-X-SKIP:SKIPPED-SEGMENTS="...)
		b = strconv.AppendInt(b, int64(skipped), 10)
		b = append(b, '\n')
	}
	currentMap := ""
	for i := skipped; i < len(t.segments); i++ {
		b = appendMediaSegment(b, t.segments[i], i == skipped, &currentMap)
	}
	if lowLatency {
		if t.initName != "" && t.initName != currentMap {
			b = appendQuotedLine(b, "#EXT-X-MAP:URI=", t.initName)
		}
		for _, part := range t.parts {
			b = appendPart(b, part)
		}
		if !endList && t.hint != nil {
			b = appendQuotedLine(b, "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=", t.hint.Name)
		}
	}
	if endList {
		b = append(b, "#EXT-X-ENDLIST\n"...)
	}
	return b
}

func appendMediaHeader(dst []byte, t *track, lowLatency, endList bool) []byte {
	target := t.target
	if target == 0 {
		target = 1
	}
	version := 7
	if lowLatency {
		version = 9
	}
	dst = append(dst, "#EXTM3U\n#EXT-X-VERSION:"...)
	dst = strconv.AppendInt(dst, int64(version), 10)
	dst = append(dst, "\n#EXT-X-TARGETDURATION:"...)
	dst = strconv.AppendInt(dst, int64(target), 10)
	dst = append(dst, "\n#EXT-X-MEDIA-SEQUENCE:"...)
	dst = strconv.AppendUint(dst, t.mediaSequence, 10)
	dst = append(dst, "\n#EXT-X-DISCONTINUITY-SEQUENCE:"...)
	dst = strconv.AppendUint(dst, t.discontinuitySequence, 10)
	dst = append(dst, '\n')
	if lowLatency {
		partTarget := t.partTarget
		if partTarget <= 0 {
			partTarget = 0.5
		}
		if !endList {
			dst = append(dst, "#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,CAN-SKIP-UNTIL="...)
			dst = strconv.AppendFloat(dst, float64(target)*6, 'f', 6, 64)
			dst = append(dst, ",PART-HOLD-BACK="...)
			dst = strconv.AppendFloat(dst, partTarget*2, 'f', 6, 64)
			dst = append(dst, '\n')
		}
		dst = append(dst, "#EXT-X-PART-INF:PART-TARGET="...)
		dst = strconv.AppendFloat(dst, partTarget, 'f', 6, 64)
		dst = append(dst, '\n')
	}
	return dst
}

func appendMediaSegment(dst []byte, s segment, first bool, currentMap *string) []byte {
	dst = appendMediaSegmentPrefix(dst, s, first, currentMap)
	for _, part := range s.Parts {
		dst = appendPart(dst, part)
	}
	dst = append(dst, "#EXTINF:"...)
	dst = strconv.AppendFloat(dst, s.Duration, 'f', 6, 64)
	dst = append(dst, ",\n"...)
	dst = append(dst, s.Name...)
	return append(dst, '\n')
}

func appendMediaSegmentPrefix(dst []byte, s segment, first bool, currentMap *string) []byte {
	if s.Discontinuity {
		dst = append(dst, "#EXT-X-DISCONTINUITY\n"...)
	}
	if s.InitName != "" && s.InitName != *currentMap {
		dst = appendQuotedLine(dst, "#EXT-X-MAP:URI=", s.InitName)
		*currentMap = s.InitName
	}
	if !s.At.IsZero() && (first || s.Discontinuity) {
		dst = append(dst, "#EXT-X-PROGRAM-DATE-TIME:"...)
		dst = s.At.AppendFormat(dst, programDateLayout)
		dst = append(dst, '\n')
	}
	for _, dateRange := range s.DateRanges {
		tag := dateRange.MarshalTag()
		if strings.HasPrefix(tag, "#EXT-X-DATERANGE:") && !strings.ContainsAny(tag, "\r\n") {
			dst = append(dst, tag...)
			dst = append(dst, '\n')
		}
	}
	return dst
}

func appendPart(dst []byte, part partialSegment) []byte {
	dst = append(dst, "#EXT-X-PART:DURATION="...)
	dst = strconv.AppendFloat(dst, part.Duration, 'f', 6, 64)
	dst = append(dst, ",URI="...)
	dst = strconv.AppendQuote(dst, part.Name)
	if part.Independent {
		dst = append(dst, ",INDEPENDENT=YES"...)
	}
	return append(dst, '\n')
}

func appendQuotedLine(dst []byte, prefix, value string) []byte {
	dst = append(dst, prefix...)
	dst = strconv.AppendQuote(dst, value)
	return append(dst, '\n')
}

func deltaSkipCount(t *track) int {
	if len(t.segments) < 2 {
		return 0
	}
	boundary := float64(maxInt(t.target, 1) * 6)
	total := 0.0
	for _, s := range t.segments {
		total += s.Duration
	}
	skipped := 0
	remaining := total
	for _, s := range t.segments {
		remaining -= s.Duration
		if remaining < boundary {
			break
		}
		skipped++
	}
	return skipped
}

func masterPlaylist(tracks []*track, independent bool) []byte {
	var videos []*track
	var audios []*track
	var subtitles []*track
	for _, t := range tracks {
		switch t.Kind {
		case KindVideo:
			videos = append(videos, t)
		case KindAudio:
			audios = append(audios, t)
		case KindSubtitle:
			subtitles = append(subtitles, t)
		}
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	if independent {
		b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	}

	const audioGroup = "audio"
	hasDefault := false
	for _, audio := range audios {
		if audio.Default {
			hasDefault = true
			break
		}
	}
	defaultWritten := false
	for i, a := range audios {
		lang := a.Lang
		if lang == "" {
			lang = "und"
		}
		label := a.Label
		if label == "" {
			label = a.Name
		}
		isDefault := (!hasDefault && i == 0) || (a.Default && !defaultWritten)
		defaultWritten = defaultWritten || isDefault
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=YES,CHANNELS=%q,URI=%q\n",
			audioGroup, label, lang, yesNo(isDefault), fmt.Sprintf("%d", maxInt(a.Channels, 2)), a.playlistName())
	}

	const subtitleGroup = "subtitles"
	subtitleDefaultWritten := false
	for _, subtitle := range subtitles {
		lang := subtitle.Lang
		if lang == "" {
			lang = "und"
		}
		label := subtitle.Label
		if label == "" {
			label = subtitle.Name
		}
		isDefault := subtitle.Default && !subtitleDefaultWritten && !subtitle.Forced
		subtitleDefaultWritten = subtitleDefaultWritten || isDefault
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=%q,NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=YES,FORCED=%s,URI=%q\n",
			subtitleGroup, label, lang, yesNo(isDefault), yesNo(subtitle.Forced), subtitle.playlistName())
	}

	if len(videos) == 0 {
		for _, a := range audios {
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q\n%s\n",
				maxInt(a.Bandwidth, 1), a.Codec, a.playlistName())
		}
		return []byte(b.String())
	}

	for _, video := range videos {
		codecs := video.Codec
		bandwidth := video.Bandwidth
		if len(audios) > 0 {
			codecs += "," + audios[0].Codec
			bandwidth += audios[0].Bandwidth
		}
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q", maxInt(bandwidth, 1), codecs)
		if video.Width > 0 && video.Height > 0 {
			fmt.Fprintf(&b, ",RESOLUTION=%dx%d", video.Width, video.Height)
		}
		if video.FrameRate > 0 {
			fmt.Fprintf(&b, ",FRAME-RATE=%.3f", video.FrameRate)
		}
		if len(audios) > 0 {
			fmt.Fprintf(&b, ",AUDIO=%q", audioGroup)
		}
		if len(subtitles) > 0 {
			fmt.Fprintf(&b, ",SUBTITLES=%q", subtitleGroup)
		}
		b.WriteByte('\n')
		b.WriteString(video.playlistName())
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func yesNo(v bool) string {
	if v {
		return "YES"
	}
	return "NO"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
