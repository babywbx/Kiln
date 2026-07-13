package packager

import (
	"fmt"
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

	Audios []mpd.Representation

	SkippedAudios []string

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

const maxNativeAudioTracks = 16

func PlanFromManifest(p *mpd.Presentation, preferHeight int) (Plan, error) {
	if len(p.Periods) != 1 {
		return unsupportedPlan(ReasonMultiPeriod, true), nil
	}
	video, audios := selectTracks(p.Periods[0].Representations, preferHeight)
	if video.ID == "" {
		return unsupportedPlan(ReasonNoVideo, true), nil
	}
	if len(audios) == 0 {
		return unsupportedPlan(ReasonNoAudio, true), nil
	}
	if !nativeAddressing(video) {
		return unsupportedPlan(ReasonAddressing, true), nil
	}
	if _, ok := nativeVideoCodecs[family(video.Codecs)]; !ok {
		return unsupportedPlan(ReasonManifestCodec, true), nil
	}

	plan := Plan{Engine: EngineNativeRewrite, Video: video, FallbackAllowed: true}
	plan.UnknownEssential = unknownEssential(append([]mpd.Representation{video}, audios...)...)
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
	if len(audios) != len(plan.Audios) {
		return fmt.Errorf("planned %d audio tracks but read %d init segments", len(plan.Audios), len(audios))
	}
	pairs := []trackPair{{plan.Video, video}}
	for i, a := range audios {
		pairs = append(pairs, trackPair{plan.Audios[i], a})
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

func selectTracks(reps []mpd.Representation, preferHeight int) (mpd.Representation, []mpd.Representation) {
	var video mpd.Representation
	var videoSet bool
	var groups []string
	best := map[string]mpd.Representation{}

	for _, rep := range reps {
		if essentialBlocked(rep) {
			continue
		}
		switch {
		case rep.IsVideo():
			if rep.Trick {
				continue
			}
			if !videoSet || betterVideo(rep, video, preferHeight) {
				video, videoSet = rep, true
			}
		case rep.IsAudio():
			cur, seen := best[rep.Group]
			if !seen {
				groups = append(groups, rep.Group)
			}
			if !seen || rep.Bandwidth > cur.Bandwidth {
				best[rep.Group] = rep
			}
		}
	}

	audios := make([]mpd.Representation, 0, len(groups))
	for _, g := range groups {
		audios = append(audios, best[g])
	}
	return video, audios
}

func betterVideo(candidate, current mpd.Representation, preferHeight int) bool {
	if preferHeight <= 0 {
		if candidate.Height != current.Height {
			return candidate.Height > current.Height
		}
		return candidate.Bandwidth > current.Bandwidth
	}
	cFits := candidate.Height <= preferHeight
	curFits := current.Height <= preferHeight
	switch {
	case cFits && !curFits:
		return true
	case !cFits && curFits:
		return false
	case cFits && curFits:
		if candidate.Height != current.Height {
			return candidate.Height > current.Height
		}
		return candidate.Bandwidth > current.Bandwidth
	default:
		if candidate.Height != current.Height {
			return candidate.Height < current.Height
		}
		return candidate.Bandwidth < current.Bandwidth
	}
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
