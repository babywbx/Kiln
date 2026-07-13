package packager

import (
	"fmt"
	"sort"
	"strings"

	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

const (
	EngineNativeRewrite  = "native_rewrite"
	EngineFFmpegCopy     = "ffmpeg_copy"
	EngineFFmpegTranscod = "ffmpeg_transcode"
)

const (
	ReasonNoVideo        = "no_video_representation"
	ReasonNoAudio        = "no_audio_representation"
	ReasonMultiPeriod    = "multi_period"
	ReasonAddressing     = "addressing_unsupported"
	ReasonManifestCodec  = "manifest_codec_unsupported"
	ReasonKIDMismatch    = "manifest_kid_conflicts_with_tenc"
	ReasonMissingKey     = "missing_key_for_kid"
	ReasonMultiKIDNoFall = "multi_kid_cannot_fall_back"

	ReasonNativeStartFailed = "native_start_failed"
)

type Plan struct {
	Engine string
	Video  mpd.Representation
	Videos []mpd.Representation

	Audios []mpd.Representation
	Texts  []mpd.Representation

	SkippedAudios []string
	SkippedText   []string

	UnknownEssential []string

	FallbackAllowed bool
	Reason          string
}

func (p Plan) Native() bool { return p.Engine == EngineNativeRewrite }

// A rendition we cannot honour, not one we merely do not understand.
var blockedEssential = map[string]struct{}{
	"urn:mpeg:dash:srd:2014": {},
}

var benignEssential = map[string]struct{}{
	"urn:mpeg:mpegb:cicp:colourprimaries":         {},
	"urn:mpeg:mpegb:cicp:transfercharacteristics": {},
	"urn:mpeg:mpegb:cicp:matrixcoefficients":      {},
	"urn:mpeg:mpegb:cicp:videofullrangeflag":      {},
	"urn:dvb:dash:lowlatency:critical:2019":       {},
}

func essentialBlocked(rep mpd.Representation) bool {
	for _, scheme := range rep.Essential {
		if _, ok := blockedEssential[scheme]; ok {
			return true
		}
	}
	return false
}

func unknownEssential(reps ...mpd.Representation) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, rep := range reps {
		for _, scheme := range rep.Essential {
			if _, ok := benignEssential[scheme]; ok {
				continue
			}
			if _, ok := seen[scheme]; ok {
				continue
			}
			seen[scheme] = struct{}{}
			out = append(out, scheme)
		}
	}
	return out
}

var nativeVideoCodecs = map[string]struct{}{
	"avc1": {}, "avc3": {}, "hvc1": {}, "hev1": {},
}

var nativeAudioCodecs = map[string]struct{}{"mp4a": {}}
var nativeTextCodecs = map[string]struct{}{"stpp": {}}

const maxNativeAudioTracks = 16
const maxNativeVideoTracks = 8
const maxNativeTextTracks = 16

func PlanFromManifest(p *mpd.Presentation, preferHeight int) (Plan, error) {
	if len(p.Periods) != 1 {
		return unsupportedPlan(ReasonMultiPeriod, true), nil
	}
	videos, audios, texts := selectRenditions(p.Periods[0].Representations, preferHeight)
	if len(videos) == 0 {
		return unsupportedPlan(ReasonNoVideo, true), nil
	}
	video := videos[len(videos)-1]
	if len(audios) == 0 {
		return unsupportedPlan(ReasonNoAudio, true), nil
	}

	plan := Plan{Engine: EngineNativeRewrite, Video: video, Videos: videos, FallbackAllowed: true}
	for _, a := range audios {
		if _, ok := nativeAudioCodecs[family(a.Codecs)]; !ok || !nativeAddressing(a) {
			plan.SkippedAudios = append(plan.SkippedAudios, a.ID)
			continue
		}
		if len(plan.Audios) >= maxNativeAudioTracks {
			plan.SkippedAudios = append(plan.SkippedAudios, a.ID)
			continue
		}
		plan.Audios = append(plan.Audios, a)
	}
	if len(plan.Audios) == 0 {
		return unsupportedPlan(ReasonManifestCodec, true), nil
	}
	for _, text := range texts {
		if _, ok := nativeTextCodecs[family(text.Codecs)]; !ok || !nativeAddressing(text) {
			plan.SkippedText = append(plan.SkippedText, text.ID)
			continue
		}
		if len(plan.Texts) >= maxNativeTextTracks {
			plan.SkippedText = append(plan.SkippedText, text.ID)
			continue
		}
		plan.Texts = append(plan.Texts, text)
	}
	selected := append([]mpd.Representation(nil), plan.Videos...)
	selected = append(selected, plan.Audios...)
	selected = append(selected, plan.Texts...)
	plan.UnknownEssential = unknownEssential(selected...)
	return plan, nil
}

func nativeAddressing(rep mpd.Representation) bool {
	switch rep.Addressing.Mode {
	case mpd.AddressingTemplateTimeline, mpd.AddressingTemplateDuration, mpd.AddressingList:
		return true
	default:
		return false
	}
}

type trackPair struct {
	rep   mpd.Representation
	track cmaf.Track
}

func VerifyTracks(plan *Plan, video cmaf.Track, audios []cmaf.Track, keys cmaf.KeySet) error {
	if len(plan.Videos) == 0 && plan.Video.ID != "" {
		plan.Videos = []mpd.Representation{plan.Video}
	}
	return VerifyTrackSet(plan, []cmaf.Track{video}, audios, nil, keys)
}

// VerifyTrackSet checks the init segments for every rendition selected by the
// planner. All variants share one key policy so a multi-KID ladder cannot
// silently fall back through a single-key engine.
func VerifyTrackSet(plan *Plan, videos, audios, texts []cmaf.Track, keys cmaf.KeySet) error {
	if len(videos) != len(plan.Videos) {
		return fmt.Errorf("planned %d video tracks but read %d init segments", len(plan.Videos), len(videos))
	}
	if len(audios) != len(plan.Audios) {
		return fmt.Errorf("planned %d audio tracks but read %d init segments", len(plan.Audios), len(audios))
	}
	if len(texts) != len(plan.Texts) {
		return fmt.Errorf("planned %d text tracks but read %d init segments", len(plan.Texts), len(texts))
	}
	pairs := make([]trackPair, 0, len(videos)+len(audios)+len(texts))
	for i, video := range videos {
		pairs = append(pairs, trackPair{plan.Videos[i], video})
	}
	for i, a := range audios {
		pairs = append(pairs, trackPair{plan.Audios[i], a})
	}
	for i, text := range texts {
		pairs = append(pairs, trackPair{plan.Texts[i], text})
	}

	kids := map[string]struct{}{}
	for _, pair := range pairs {
		if !pair.track.Encrypted {
			continue
		}
		if pair.rep.DefaultKID != "" && pair.rep.DefaultKID != pair.track.KID {
			plan.FallbackAllowed = true
			plan.Reason = ReasonKIDMismatch
			return fmt.Errorf("manifest default_KID %s conflicts with tenc %s on %s",
				pair.rep.DefaultKID, pair.track.KID, pair.rep.ID)
		}
		if _, ok := keys[pair.track.KID]; !ok {
			plan.FallbackAllowed = true
			plan.Reason = ReasonMissingKey
			return fmt.Errorf("no key for kid %s (%s)", pair.track.KID, pair.rep.ID)
		}
		kids[pair.track.KID] = struct{}{}
	}

	if len(kids) > 1 {
		plan.FallbackAllowed = false
	}
	return nil
}

func selectRenditions(reps []mpd.Representation, preferHeight int) ([]mpd.Representation, []mpd.Representation, []mpd.Representation) {
	var groups []string
	best := map[string]mpd.Representation{}
	var videoCandidates []mpd.Representation
	var texts []mpd.Representation

	for _, rep := range reps {
		if essentialBlocked(rep) {
			continue
		}
		switch {
		case rep.IsVideo():
			if rep.Trick {
				continue
			}
			if _, ok := nativeVideoCodecs[family(rep.Codecs)]; ok && nativeAddressing(rep) {
				videoCandidates = append(videoCandidates, rep)
			}
		case rep.IsAudio():
			cur, seen := best[rep.Group]
			if !seen {
				groups = append(groups, rep.Group)
			}
			if !seen || rep.Bandwidth > cur.Bandwidth {
				best[rep.Group] = rep
			}
		case rep.IsText():
			texts = append(texts, rep)
		}
	}

	videos := abrLadder(videoCandidates, preferHeight)
	audios := make([]mpd.Representation, 0, len(groups))
	for _, g := range groups {
		audios = append(audios, best[g])
	}
	return videos, audios, texts
}

func abrLadder(candidates []mpd.Representation, preferHeight int) []mpd.Representation {
	if len(candidates) == 0 {
		return nil
	}
	bestBySize := make(map[string]mpd.Representation, len(candidates))
	for _, candidate := range candidates {
		if preferHeight > 0 && candidate.Height > preferHeight {
			continue
		}
		key := fmt.Sprintf("%dx%d", candidate.Width, candidate.Height)
		if current, found := bestBySize[key]; !found || candidate.Bandwidth > current.Bandwidth {
			bestBySize[key] = candidate
		}
	}
	if len(bestBySize) == 0 {
		lowest := candidates[0]
		for _, candidate := range candidates[1:] {
			if candidate.Height < lowest.Height ||
				(candidate.Height == lowest.Height && candidate.Bandwidth < lowest.Bandwidth) {
				lowest = candidate
			}
		}
		bestBySize[fmt.Sprintf("%dx%d", lowest.Width, lowest.Height)] = lowest
	}
	ladder := make([]mpd.Representation, 0, len(bestBySize))
	for _, representation := range bestBySize {
		ladder = append(ladder, representation)
	}
	sort.SliceStable(ladder, func(i, j int) bool {
		if ladder[i].Height != ladder[j].Height {
			return ladder[i].Height < ladder[j].Height
		}
		return ladder[i].Bandwidth < ladder[j].Bandwidth
	})
	if len(ladder) > maxNativeVideoTracks {
		ladder = ladder[len(ladder)-maxNativeVideoTracks:]
	}
	return ladder
}

func unsupportedPlan(reason string, fallback bool) Plan {
	return Plan{Engine: EngineFFmpegCopy, FallbackAllowed: fallback, Reason: reason}
}

func family(codecs string) string {
	c := strings.ToLower(strings.TrimSpace(codecs))
	if i := strings.Index(c, "."); i > 0 {
		return c[:i]
	}
	return c
}
