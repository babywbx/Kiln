package packager

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/babywbx/kiln/modules/config"
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

	Audios          []mpd.Representation
	Texts           []mpd.Representation
	DefaultAudioKey string
	DefaultTextKey  string

	SkippedAudios []string
	SkippedText   []string

	UnknownEssential []string

	FallbackAllowed bool
	Reason          string
}

func (p Plan) Native() bool { return p.Engine == EngineNativeRewrite }

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
	return PlanFromManifestWithSelection(p, preferHeight, config.TrackSelection{})
}

func PlanFromManifestWithSelection(p *mpd.Presentation, preferHeight int, selection config.TrackSelection) (Plan, error) {
	if len(p.Periods) != 1 {
		return unsupportedPlan(ReasonMultiPeriod, true), nil
	}
	if reason, needed := selectedTrackNeedsCompatibility(p.Periods[0].Representations, selection); needed {
		return unsupportedPlan(reason, true), nil
	}
	videos, audios, texts, err := selectRenditions(p.Periods[0].Representations, preferHeight, selection)
	if err != nil {
		return Plan{}, err
	}
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
	plan.DefaultAudioKey = selectedDefaultAudio(plan.Audios, selection.Audio)
	plan.DefaultTextKey = selectedDefaultText(plan.Texts, selection.Subtitles)
	selected := append([]mpd.Representation(nil), plan.Videos...)
	selected = append(selected, plan.Audios...)
	selected = append(selected, plan.Texts...)
	plan.UnknownEssential = unknownEssential(selected...)
	return plan, nil
}

func selectedTrackNeedsCompatibility(reps []mpd.Representation, selection config.TrackSelection) (string, bool) {
	checks := []struct {
		mode     string
		selector config.TrackSelector
		kind     mpd.ContentType
	}{
		{selection.Video.Mode, selection.Video.Track, mpd.TypeVideo},
		{selection.Audio.Mode, selection.Audio.Track, mpd.TypeAudio},
		{selection.Subtitles.Mode, selection.Subtitles.Track, mpd.TypeText},
	}
	for _, check := range checks {
		mode := normalizedMode(check.mode, "auto")
		if !hasSelector(check.selector) || mode == "auto" || mode == "off" {
			continue
		}
		selected, ok := findSelected(repsOfType(reps, check.kind), check.selector)
		if !ok {
			continue
		}
		if supported, reason := nativeTrackSupport(selected); !supported {
			if reason == ReasonAddressing || strings.Contains(strings.ToLower(reason), "address") {
				return ReasonAddressing, true
			}
			return ReasonManifestCodec, true
		}
	}
	return "", false
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

func selectRenditions(reps []mpd.Representation, preferHeight int, selection config.TrackSelection) ([]mpd.Representation, []mpd.Representation, []mpd.Representation, error) {
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

	videoMode := normalizedMode(selection.Video.Mode, "auto")
	if selection.Video.MaxHeight > 0 {
		preferHeight = selection.Video.MaxHeight
	}
	if videoMode == "cap" || videoMode == "exact" {
		selected, ok := findSelected(videoCandidates, selection.Video.Track)
		if !ok && hasSelector(selection.Video.Track) {
			return nil, nil, nil, fmt.Errorf("selected video track is no longer present")
		}
		if ok {
			preferHeight = selected.Height
			videoCandidates = capVideoCandidates(videoCandidates, selected)
		}
	}
	videos := abrLadder(videoCandidates, preferHeight)
	audios := make([]mpd.Representation, 0, len(groups))
	for _, g := range groups {
		audios = append(audios, best[g])
	}
	audioMode := normalizedMode(selection.Audio.Mode, "auto")
	if audioMode == "prefer" || audioMode == "only" {
		selected, ok := findSelected(repsOfType(reps, mpd.TypeAudio), selection.Audio.Track)
		if !ok && hasSelector(selection.Audio.Track) {
			return nil, nil, nil, fmt.Errorf("selected audio track is no longer present")
		}
		if ok {
			if audioMode == "only" {
				audios = []mpd.Representation{selected}
			} else {
				for i := range audios {
					if audios[i].Group == selected.Group {
						audios[i] = selected
					}
				}
			}
		}
	}
	subtitleMode := normalizedMode(selection.Subtitles.Mode, "auto")
	switch subtitleMode {
	case "off":
		texts = nil
	case "prefer", "only":
		selected, ok := findSelected(texts, selection.Subtitles.Track)
		if !ok && hasSelector(selection.Subtitles.Track) {
			return nil, nil, nil, fmt.Errorf("selected subtitle track is no longer present")
		}
		if ok && subtitleMode == "only" {
			texts = []mpd.Representation{selected}
		}
	}
	return videos, audios, texts, nil
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
		key := fmt.Sprintf("%dx%d@%s", candidate.Width, candidate.Height, normalizedFrameRate(candidate.FrameRate))
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
		bestBySize[fmt.Sprintf("%dx%d@%s", lowest.Width, lowest.Height, normalizedFrameRate(lowest.FrameRate))] = lowest
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

func capVideoCandidates(candidates []mpd.Representation, selected mpd.Representation) []mpd.Representation {
	selectedFPS := frameRateValue(selected.FrameRate)
	out := make([]mpd.Representation, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Height > selected.Height {
			continue
		}
		if candidate.Height == selected.Height && selectedFPS > 0 && frameRateValue(candidate.FrameRate) > selectedFPS+0.01 {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func repsOfType(reps []mpd.Representation, kind mpd.ContentType) []mpd.Representation {
	out := make([]mpd.Representation, 0, len(reps))
	for _, rep := range reps {
		if rep.Type == kind {
			out = append(out, rep)
		}
	}
	return out
}

func findSelected(reps []mpd.Representation, selector config.TrackSelector) (mpd.Representation, bool) {
	var keyMatch mpd.Representation
	keyMatches := 0
	for _, rep := range reps {
		if selector.Key != "" && trackIdentity(rep) == selector.Key {
			keyMatch = rep
			keyMatches++
		}
	}
	if keyMatches == 1 {
		return keyMatch, true
	}
	if keyMatches > 1 {
		return mpd.Representation{}, false
	}
	var semanticMatch mpd.Representation
	semanticMatches := 0
	for _, rep := range reps {
		if selector.RepresentationID != "" && rep.ID != selector.RepresentationID {
			continue
		}
		if selector.AdaptationSetID != "" && rep.AdaptationSetID != selector.AdaptationSetID {
			continue
		}
		if selector.Language != "" && !strings.EqualFold(rep.Lang, selector.Language) {
			continue
		}
		if selector.Role != "" && !hasRole(rep, selector.Role) {
			continue
		}
		if selector.Codec != "" && !strings.EqualFold(rep.Codecs, selector.Codec) {
			continue
		}
		if selector.Height > 0 && rep.Height != selector.Height {
			continue
		}
		if selector.FrameRate != "" && normalizedFrameRate(rep.FrameRate) != normalizedFrameRate(selector.FrameRate) {
			continue
		}
		if hasSelector(selector) {
			semanticMatch = rep
			semanticMatches++
		}
	}
	return semanticMatch, semanticMatches == 1
}

func hasSelector(selector config.TrackSelector) bool {
	return selector.Key != "" || selector.AdaptationSetID != "" || selector.RepresentationID != "" ||
		selector.Language != "" || selector.Role != "" || selector.Codec != "" ||
		selector.Height > 0 || selector.FrameRate != ""
}

func selectedDefaultAudio(audios []mpd.Representation, selection config.AudioSelection) string {
	if selected, ok := findSelected(audios, selection.Track); ok {
		return trackIdentity(selected)
	}
	for _, preferred := range selection.PreferredLanguages {
		for _, audio := range audios {
			if strings.EqualFold(audio.Lang, preferred) {
				return trackIdentity(audio)
			}
		}
	}
	for _, audio := range audios {
		if hasRole(audio, "main") {
			return trackIdentity(audio)
		}
	}
	if len(audios) > 0 {
		return trackIdentity(audios[0])
	}
	return ""
}

func selectedDefaultText(texts []mpd.Representation, selection config.SubtitleSelection) string {
	if normalizedMode(selection.Mode, "auto") != "prefer" && normalizedMode(selection.Mode, "auto") != "only" {
		return ""
	}
	if selected, ok := findSelected(texts, selection.Track); ok {
		return trackIdentity(selected)
	}
	return ""
}

func trackIdentity(rep mpd.Representation) string {
	if rep.TrackKey != "" {
		return rep.TrackKey
	}
	return rep.Group + "\x1f" + rep.ID
}

func normalizedMode(mode, fallback string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return fallback
	}
	return mode
}

func hasRole(rep mpd.Representation, wanted string) bool {
	for _, role := range rep.Roles {
		if strings.EqualFold(strings.TrimSpace(role), wanted) {
			return true
		}
	}
	return false
}

func normalizedFrameRate(value string) string {
	fps := frameRateValue(value)
	if fps <= 0 {
		return strings.TrimSpace(value)
	}
	return strconv.FormatFloat(fps, 'f', 3, 64)
}

func frameRateValue(value string) float64 {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if numerator, denominator, ok := strings.Cut(value, "/"); ok {
		n, nErr := strconv.ParseFloat(numerator, 64)
		d, dErr := strconv.ParseFloat(denominator, 64)
		if nErr == nil && dErr == nil && d != 0 {
			return n / d
		}
	}
	fps, _ := strconv.ParseFloat(value, 64)
	if math.IsNaN(fps) || math.IsInf(fps, 0) {
		return 0
	}
	return fps
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
