package hls

import (
	"fmt"
	"math"
	"strings"
)

// MasterName is the entry playlist every player starts from.
const MasterName = "master.m3u8"

// targetDuration is the rounded-up maximum segment duration in the window, as
// RFC 8216 requires; a too-small value makes players stall.
func targetDuration(segs []segment) int {
	var max float64
	for _, s := range segs {
		if s.Duration > max {
			max = s.Duration
		}
	}
	if max <= 0 {
		return 1
	}
	return int(math.Ceil(max - 0.001))
}

func mediaPlaylist(t *track, endList bool) []byte {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", targetDuration(t.segments))
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", t.mediaSequence)
	fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", t.discontinuitySequence)

	// The map is re-emitted whenever it changes, so segments that decode against
	// an older init keep working after the stream switches to a new one.
	currentMap := ""
	for _, s := range t.segments {
		if s.Discontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if s.InitName != currentMap {
			fmt.Fprintf(&b, "#EXT-X-MAP:URI=%q\n", s.InitName)
			currentMap = s.InitName
		}
		fmt.Fprintf(&b, "#EXTINF:%.6f,\n", s.Duration)
		b.WriteString(s.Name)
		b.WriteByte('\n')
	}
	if endList {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	return []byte(b.String())
}

func masterPlaylist(tracks []*track, independent bool) []byte {
	var video *track
	var audios []*track
	for _, t := range tracks {
		switch t.Kind {
		case KindVideo:
			if video == nil {
				video = t
			}
		case KindAudio:
			audios = append(audios, t)
		}
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:7\n")
	if independent {
		b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	}

	const audioGroup = "audio"
	for i, a := range audios {
		lang := a.Lang
		if lang == "" {
			lang = "und"
		}
		fmt.Fprintf(&b, "#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=%q,NAME=%q,LANGUAGE=%q,DEFAULT=%s,AUTOSELECT=YES,CHANNELS=%q,URI=%q\n",
			audioGroup, a.Name, lang, yesNo(i == 0), fmt.Sprintf("%d", maxInt(a.Channels, 2)), a.playlistName())
	}

	if video == nil {
		for _, a := range audios {
			fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,CODECS=%q\n%s\n",
				maxInt(a.Bandwidth, 1), a.Codec, a.playlistName())
		}
		return []byte(b.String())
	}

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
	b.WriteByte('\n')
	b.WriteString(video.playlistName())
	b.WriteByte('\n')
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
