package packager

import (
	"fmt"
	"strings"

	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

// Engine names are stable and surfaced through the status API.
const (
	EngineNativeRewrite  = "native_rewrite"
	EngineFFmpegCopy     = "ffmpeg_copy"
	EngineFFmpegTranscod = "ffmpeg_transcode"
)

// Fallback reasons that originate in the manifest rather than the init segment.
const (
	ReasonNoVideo        = "no_video_representation"
	ReasonNoAudio        = "no_audio_representation"
	ReasonMultiPeriod    = "multi_period"
	ReasonAddressing     = "addressing_unsupported"
	ReasonManifestCodec  = "manifest_codec_unsupported"
	ReasonKIDMismatch    = "manifest_kid_conflicts_with_tenc"
	ReasonMissingKey     = "missing_key_for_kid"
	ReasonMultiKIDNoFall = "multi_kid_cannot_fall_back"
	// ReasonNativeStartFailed covers failures that are not about capability at
	// all, such as an unreachable manifest. ffmpeg resolves the manifest on a
	// different path, so it is still worth trying.
	ReasonNativeStartFailed = "native_start_failed"
)

// Plan is the one-shot capability decision. Engine selection happens at
// startup only; a running publication never switches engines.
type Plan struct {
	Engine string
	Video  mpd.Representation
	// Audios is one representation per audio adaptation set, in manifest order.
	// The first one is what a player picks when it has no preference.
	Audios []mpd.Representation
	// SkippedAudios names the audio adaptation sets left out, so a channel that
	// silently ships fewer languages than the source has says why.
	SkippedAudios []string
	// FallbackAllowed is not a global switch. It is false when ffmpeg would
	// produce a worse result than an honest failure, e.g. multi-KID input,
	// where ffmpeg is handed a single key and decodes garbage.
	FallbackAllowed bool
	Reason          string
}

func (p Plan) Native() bool { return p.Engine == EngineNativeRewrite }

var nativeVideoCodecs = map[string]struct{}{
	"avc1": {}, "avc3": {}, "hvc1": {}, "hev1": {},
}

var nativeAudioCodecs = map[string]struct{}{"mp4a": {}}

// PlanFromManifest picks tracks and decides whether the native path can even be
// attempted. It only sees the manifest; the init segments get the final say.
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

	// An audio the native path cannot carry is dropped, not fatal: a source that
	// offers a second language in E-AC-3 should still serve the languages it
	// offers in AAC, rather than sending the whole channel to ffmpeg, which would
	// carry exactly one of them anyway.
	plan := Plan{Engine: EngineNativeRewrite, Video: video, FallbackAllowed: true}
	for _, a := range audios {
		if _, ok := nativeAudioCodecs[family(a.Codecs)]; !ok || !nativeAddressing(a) {
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

// trackPair is one representation next to what its init segment actually says.
type trackPair struct {
	rep   mpd.Representation
	track cmaf.Track
}

// VerifyTracks is the second gate: the init segments carry the truth about
// encryption scheme, sample entry and KID, and the manifest may disagree.
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

	// ffmpeg takes a single -cenc_decryption_key and silently falls back to
	// the first key on a KID miss, so handing it a multi-KID stream produces a
	// stream that starts and then decodes to garbage. Failing is better.
	if len(kids) > 1 {
		plan.FallbackAllowed = false
	}
	return nil
}

// selectTracks picks the video rendition to carry, and one audio rendition per
// audio adaptation set. Two representations in the same set are the same audio
// at two bitrates, so only the best of each is taken; two sets are two different
// audios, and both are carried.
func selectTracks(reps []mpd.Representation, preferHeight int) (mpd.Representation, []mpd.Representation) {
	var video mpd.Representation
	var videoSet bool
	var groups []string
	best := map[string]mpd.Representation{}

	for _, rep := range reps {
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

// betterVideo prefers the tallest rendition at or below preferHeight, and the
// shortest one above it when nothing fits.
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
