package hls

import (
	"fmt"
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
		b = fmt.Appendf(b, "#EXT-X-SKIP:SKIPPED-SEGMENTS=%d\n", skipped)
	}
	currentMap := ""
	for i := skipped; i < len(t.segments); i++ {
		b = appendMediaSegment(b, t.segments[i], i == skipped, &currentMap)
	}
	if lowLatency {
		if t.initName != "" && t.initName != currentMap {
			b = fmt.Appendf(b, "#EXT-X-MAP:URI=%q\n", t.initName)
		}
		for _, part := range t.parts {
			b = appendPart(b, part)
		}
		if !endList && t.hint != nil {
			b = fmt.Appendf(b, "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=%q\n", t.hint.Name)
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
	dst = fmt.Appendf(dst, "#EXTM3U\n#EXT-X-VERSION:%d\n", version)
	dst = fmt.Appendf(dst, "#EXT-X-TARGETDURATION:%d\n", target)
	dst = fmt.Appendf(dst, "#EXT-X-MEDIA-SEQUENCE:%d\n", t.mediaSequence)
	dst = fmt.Appendf(dst, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", t.discontinuitySequence)
	if lowLatency {
		partTarget := t.partTarget
		if partTarget <= 0 {
			partTarget = 0.5
		}
		if !endList {
			dst = fmt.Appendf(dst, "#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,CAN-SKIP-UNTIL=%.6f,PART-HOLD-BACK=%.6f\n", float64(target)*6, partTarget*2)
		}
		dst = fmt.Appendf(dst, "#EXT-X-PART-INF:PART-TARGET=%.6f\n", partTarget)
	}
	return dst
}

func appendMediaSegment(dst []byte, s segment, first bool, currentMap *string) []byte {
	dst = appendMediaSegmentPrefix(dst, s, first, currentMap)
	for _, part := range s.Parts {
		dst = appendPart(dst, part)
	}
	dst = fmt.Appendf(dst, "#EXTINF:%.6f,\n", s.Duration)
	dst = append(dst, s.Name...)
	return append(dst, '\n')
}

func appendMediaSegmentPrefix(dst []byte, s segment, first bool, currentMap *string) []byte {
	if s.Discontinuity {
		dst = append(dst, "#EXT-X-DISCONTINUITY\n"...)
	}
	if s.InitName != "" && s.InitName != *currentMap {
		dst = fmt.Appendf(dst, "#EXT-X-MAP:URI=%q\n", s.InitName)
		*currentMap = s.InitName
	}
	if !s.At.IsZero() && (first || s.Discontinuity) {
		dst = fmt.Appendf(dst, "#EXT-X-PROGRAM-DATE-TIME:%s\n", s.At.Format(programDateLayout))
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
	dst = fmt.Appendf(dst, "#EXT-X-PART:DURATION=%.6f,URI=%q", part.Duration, part.Name)
	if part.Independent {
		dst = append(dst, ",INDEPENDENT=YES"...)
	}
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
	for i, subtitle := range subtitles {
		lang := subtitle.Lang
		if lang == "" {
			lang = "und"
		}
		label := subtitle.Label
		if label == "" {
			label = subtitle.Name
		}
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=%q,NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=YES,FORCED=NO,URI=%q\n",
			subtitleGroup, label, lang, yesNo(i == 0), subtitle.playlistName())
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
