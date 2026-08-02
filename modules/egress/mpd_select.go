package egress

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
)

type videoRep struct {
	full      string
	id        string
	bandwidth int
	height    int
	trick     bool
}

type audioRep struct {
	full string
	id   string
}

var (
	repTagRe            = regexp.MustCompile(`(?s)<Representation\b[^>]*?/>|<Representation\b[^>]*>.*?</Representation>`)
	attrRe              = regexp.MustCompile(`(\w+)="([^"]*)"`)
	baseURLRe           = regexp.MustCompile(`(?i)<BaseURL>[^<]*</BaseURL>`)
	segmentTimelineRe   = regexp.MustCompile(`(?is)<SegmentTimeline>\s*(.*?)\s*</SegmentTimeline>`)
	segmentTimelineSRe  = regexp.MustCompile(`(?is)<S\b[^>]*?/>`)
	adaptationSetRe     = regexp.MustCompile(`(?is)<AdaptationSet\b[^>]*?/>|<AdaptationSet\b[^>]*>.*?</AdaptationSet>`)
	defaultTimelineKeep = 10
)

func dropEmptyAdaptationSets(mpd string) string {
	out := mpd
	for _, set := range adaptationSetRe.FindAllString(mpd, -1) {
		if !repTagRe.MatchString(set) {
			out = strings.Replace(out, set, "", 1)
		}
	}
	return out
}

type StreamPick struct {
	VideoIndex int
	AudioIndex int
	VideoID    string
	Height     int
	Bandwidth  int
	Dynamic    bool
}

func PickStreams(mpd string, preferHeight int) StreamPick {
	pick, _ := PickStreamsWithSelection(mpd, preferHeight, "", "")
	return pick
}

func PickStreamsWithSelection(mpd string, preferHeight int, videoID, audioID string) (StreamPick, error) {
	reps := repTagRe.FindAllString(mpd, -1)
	var videos []videoRep
	audioCount := 0
	firstAudio := -1
	selectedAudio := -1
	selectedAudioMatches := 0
	for _, r := range reps {
		openEnd := strings.Index(r, ">")
		if openEnd < 0 {
			continue
		}
		attrs := parseAttrs(r[:openEnd+1])
		id := attrs["id"]
		h, _ := strconv.Atoi(attrs["height"])
		w, _ := strconv.Atoi(attrs["width"])
		bw, _ := strconv.Atoi(attrs["bandwidth"])
		codecs := strings.ToLower(attrs["codecs"])
		trick := strings.Contains(strings.ToLower(id), "trick") || attrs["maxplayoutrate"] != ""
		isSub := strings.Contains(codecs, "stpp") || strings.Contains(codecs, "wvtt")
		isAudio := strings.Contains(codecs, "mp4a") || attrs["audiosamplingrate"] != "" ||
			(h == 0 && w == 0 && (strings.HasPrefix(strings.ToLower(id), "a") || strings.HasPrefix(strings.ToLower(id), "au")))
		isVideo := h > 0 || w > 0 || strings.Contains(codecs, "hev") || strings.Contains(codecs, "hvc") ||
			strings.Contains(codecs, "avc") || strings.Contains(codecs, "vp09") || strings.Contains(codecs, "av01")
		if isSub {
			continue
		}
		if isAudio && !isVideo {
			if firstAudio < 0 {
				firstAudio = audioCount
			}
			if audioID != "" && id == audioID {
				selectedAudio = audioCount
				selectedAudioMatches++
			}
			audioCount++
			continue
		}
		if isVideo {
			videos = append(videos, videoRep{full: r, id: id, bandwidth: bw, height: h, trick: trick})
		}
	}
	if audioID != "" && selectedAudio < 0 {
		return StreamPick{}, fmt.Errorf("selected audio representation %q is not present", audioID)
	}
	if audioID != "" && selectedAudioMatches > 1 {
		return StreamPick{}, fmt.Errorf("selected audio representation %q is ambiguous across adaptation sets", audioID)
	}
	if selectedAudio >= 0 {
		firstAudio = selectedAudio
	}
	pick := StreamPick{VideoIndex: 0, AudioIndex: firstAudio, Dynamic: isDynamicMPD(mpd)}
	chosen := pickVideoByID(videos, preferHeight, videoID)
	if videoID != "" && videoIDCount(videos, videoID) > 1 {
		return StreamPick{}, fmt.Errorf("selected video representation %q is ambiguous across adaptation sets", videoID)
	}
	if videoID != "" && chosen == nil {
		return StreamPick{}, fmt.Errorf("selected video representation %q is not present", videoID)
	}
	if chosen == nil {
		return pick, nil
	}
	idx := 0
	for _, v := range videos {
		if v.id == chosen.id && v.height == chosen.height && v.bandwidth == chosen.bandwidth {
			pick.VideoIndex = idx
			pick.VideoID = chosen.id
			pick.Height = chosen.height
			pick.Bandwidth = chosen.bandwidth
			break
		}
		idx++
	}
	return pick, nil
}

func isDynamicMPD(mpd string) bool {
	lower := strings.ToLower(mpd)
	i := strings.Index(lower, "<mpd")
	if i < 0 {
		return false
	}
	end := strings.Index(lower[i:], ">")
	if end < 0 {
		return false
	}
	open := lower[i : i+end+1]
	return strings.Contains(open, `type="dynamic"`)
}

func FilterMPDForPack(mpd, mpdURL string, preferHeight int) (string, string, error) {
	return FilterMPDForPackWithSelection(mpd, mpdURL, preferHeight, "", "")
}

func FilterMPDForPackWithSelection(mpd, mpdURL string, preferHeight int, videoID, audioID string) (string, string, error) {
	if !strings.Contains(mpd, "<MPD") && !strings.Contains(mpd, "<mpd") {
		return "", "", fmt.Errorf("not an mpd document")
	}
	reps := repTagRe.FindAllString(mpd, -1)
	if len(reps) == 0 {
		return mpd, "passthrough", nil
	}

	var videos []videoRep
	var audios []audioRep
	for _, r := range reps {
		openEnd := strings.Index(r, ">")
		if openEnd < 0 {
			continue
		}
		open := r[:openEnd+1]
		attrs := parseAttrs(open)
		id := attrs["id"]
		h, _ := strconv.Atoi(attrs["height"])
		w, _ := strconv.Atoi(attrs["width"])
		bw, _ := strconv.Atoi(attrs["bandwidth"])
		codecs := strings.ToLower(attrs["codecs"])
		trick := strings.Contains(strings.ToLower(id), "trick") || attrs["maxplayoutrate"] != ""

		isSub := strings.Contains(codecs, "stpp") || strings.Contains(codecs, "wvtt")
		isAudio := strings.Contains(codecs, "mp4a") || attrs["audiosamplingrate"] != "" ||
			(h == 0 && w == 0 && (strings.HasPrefix(strings.ToLower(id), "a") || strings.HasPrefix(strings.ToLower(id), "au")))
		isVideo := h > 0 || w > 0 || strings.Contains(codecs, "hev") || strings.Contains(codecs, "hvc") ||
			strings.Contains(codecs, "avc") || strings.Contains(codecs, "vp09") || strings.Contains(codecs, "av01")

		if isSub {
			continue
		}
		if isAudio && !isVideo {
			audios = append(audios, audioRep{full: r, id: id})
			continue
		}
		if isVideo {
			videos = append(videos, videoRep{full: r, id: id, bandwidth: bw, height: h, trick: trick})
			continue
		}
	}

	chosenVideo := pickVideoByID(videos, preferHeight, videoID)
	if videoID != "" && videoIDCount(videos, videoID) > 1 {
		return "", "", fmt.Errorf("selected video representation %q is ambiguous across adaptation sets", videoID)
	}
	if videoID != "" && chosenVideo == nil {
		return "", "", fmt.Errorf("selected video representation %q is not present", videoID)
	}
	var keep []string
	note := "none"
	if chosenVideo != nil {
		keep = append(keep, chosenVideo.full)
		note = fmt.Sprintf("video id=%s height=%d bw=%d", chosenVideo.id, chosenVideo.height, chosenVideo.bandwidth)
	}
	chosenAudio := firstAudioByID(audios, audioID)
	if audioID != "" && audioIDCount(audios, audioID) > 1 {
		return "", "", fmt.Errorf("selected audio representation %q is ambiguous across adaptation sets", audioID)
	}
	if audioID != "" && chosenAudio == nil {
		return "", "", fmt.Errorf("selected audio representation %q is not present", audioID)
	}
	if chosenAudio != nil {
		keep = append(keep, chosenAudio.full)
		note += " +audio"
	}

	out := mpd
	keepSet := map[string]struct{}{}
	for _, k := range keep {
		keepSet[k] = struct{}{}
	}
	for _, r := range reps {
		if _, ok := keepSet[r]; ok {
			continue
		}
		out = strings.Replace(out, r, "", 1)
	}
	out = dropEmptyAdaptationSets(out)

	base := resolveBaseURL(mpd, mpdURL)
	if base != "" {
		if baseURLRe.MatchString(out) {
			out = baseURLRe.ReplaceAllString(out, "<BaseURL>"+xmlEscape(base)+"</BaseURL>")
		} else if j := strings.Index(out, "<MPD"); j >= 0 {
			if k := strings.Index(out[j:], ">"); k >= 0 {
				pos := j + k + 1
				out = out[:pos] + "\n  <BaseURL>" + xmlEscape(base) + "</BaseURL>" + out[pos:]
			}
		}
	}
	if !isDynamicMPD(mpd) {
		var trimmed int
		out, trimmed = trimSegmentTimelines(out, defaultTimelineKeep)
		if trimmed > 0 {
			note += fmt.Sprintf(" timeline_trim=%d", trimmed)
		}
	}
	return out, note, nil
}

func pickVideoByID(videos []videoRep, preferHeight int, videoID string) *videoRep {
	if videoID != "" {
		for i := range videos {
			if videos[i].id == videoID && !videos[i].trick {
				return &videos[i]
			}
		}
		return nil
	}
	return pickVideo(videos, preferHeight)
}

func firstAudioByID(audios []audioRep, audioID string) *audioRep {
	if audioID == "" && len(audios) > 0 {
		return &audios[0]
	}
	for i := range audios {
		if audios[i].id == audioID {
			return &audios[i]
		}
	}
	return nil
}

func videoIDCount(videos []videoRep, id string) int {
	count := 0
	for _, video := range videos {
		if video.id == id {
			count++
		}
	}
	return count
}

func audioIDCount(audios []audioRep, id string) int {
	count := 0
	for _, audio := range audios {
		if audio.id == id {
			count++
		}
	}
	return count
}

func trimSegmentTimelines(mpd string, keep int) (string, int) {
	if keep <= 0 {
		keep = defaultTimelineKeep
	}
	dropped := 0
	out := segmentTimelineRe.ReplaceAllStringFunc(mpd, func(block string) string {
		sub := segmentTimelineRe.FindStringSubmatch(block)
		if len(sub) < 2 {
			return block
		}
		inner := sub[1]
		elems := segmentTimelineSRe.FindAllString(inner, -1)
		if len(elems) <= keep {
			return block
		}
		dropped += len(elems) - keep
		kept := elems[len(elems)-keep:]
		return "<SegmentTimeline>\n          " + strings.Join(kept, "\n          ") + "\n        </SegmentTimeline>"
	})
	return out, dropped
}

func resolveBaseURL(mpd, mpdURL string) string {
	inner := regexp.MustCompile(`(?i)<BaseURL>([^<]*)</BaseURL>`).FindStringSubmatch(mpd)
	ref := ""
	if len(inner) == 2 {
		ref = strings.TrimSpace(inner[1])
	}
	base, err := url.Parse(mpdURL)
	if err != nil {
		return dirURL(mpdURL)
	}
	base.RawQuery = ""
	base.Fragment = ""
	if strings.HasSuffix(strings.ToLower(base.Path), ".mpd") || path.Ext(base.Path) != "" {
		base.Path = path.Dir(base.Path)
	}
	if !strings.HasSuffix(base.Path, "/") {
		base.Path += "/"
	}
	if ref == "" {
		return base.String()
	}
	rel, err := url.Parse(ref)
	if err != nil {
		return base.String()
	}
	return base.ResolveReference(rel).String()
}

func pickVideo(videos []videoRep, preferHeight int) *videoRep {
	var candidates []videoRep
	for _, v := range videos {
		if !v.trick {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		if len(videos) == 0 {
			return nil
		}
		return &videos[0]
	}
	if preferHeight <= 0 {
		best := candidates[0]
		for _, v := range candidates[1:] {
			if v.height > best.height || (v.height == best.height && v.bandwidth > best.bandwidth) {
				best = v
			}
		}
		return &best
	}
	var under *videoRep
	var over *videoRep
	for i := range candidates {
		v := &candidates[i]
		if v.height <= preferHeight {
			if under == nil || v.height > under.height || (v.height == under.height && v.bandwidth > under.bandwidth) {
				under = v
			}
		} else if over == nil || v.height < over.height {
			over = v
		}
	}
	if under != nil {
		return under
	}
	return over
}

func parseAttrs(tag string) map[string]string {
	out := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(tag, -1) {
		out[strings.ToLower(m[1])] = m[2]
	}
	return out
}

func dirURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.RawQuery = ""
	u.Fragment = ""
	if strings.HasSuffix(strings.ToLower(u.Path), ".mpd") || path.Ext(u.Path) != "" {
		u.Path = path.Dir(u.Path)
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String()
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
