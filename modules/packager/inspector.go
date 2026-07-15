package packager

import (
	"context"
	"sort"
	"strings"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/packager/mpd"
)

type TrackInspection struct {
	Key               string   `json:"key"`
	AdaptationSetID   string   `json:"adaptation_set_id,omitempty"`
	RepresentationID  string   `json:"representation_id,omitempty"`
	Type              string   `json:"type"`
	Language          string   `json:"language,omitempty"`
	Roles             []string `json:"roles,omitempty"`
	Codec             string   `json:"codec,omitempty"`
	Bandwidth         int      `json:"bandwidth,omitempty"`
	Width             int      `json:"width,omitempty"`
	Height            int      `json:"height,omitempty"`
	FrameRate         float64  `json:"frame_rate,omitempty"`
	FrameRateRaw      string   `json:"frame_rate_raw,omitempty"`
	Channels          int      `json:"channels,omitempty"`
	SamplingRate      string   `json:"sampling_rate,omitempty"`
	Encrypted         bool     `json:"encrypted"`
	DefaultKID        string   `json:"default_kid,omitempty"`
	NativeSupported   bool     `json:"native_supported"`
	UnsupportedReason string   `json:"unsupported_reason,omitempty"`
	Ambiguous         bool     `json:"ambiguous,omitempty"`
}

type InspectionRecommendation struct {
	VideoKey    string `json:"video_key,omitempty"`
	AudioKey    string `json:"audio_key,omitempty"`
	SubtitleKey string `json:"subtitle_key,omitempty"`
}

type ManifestInspection struct {
	FinalURL            string                   `json:"final_url,omitempty"`
	Dynamic             bool                     `json:"dynamic"`
	PeriodCount         int                      `json:"period_count"`
	Encrypted           bool                     `json:"encrypted"`
	DefaultKIDs         []string                 `json:"default_kids,omitempty"`
	KeyStatus           string                   `json:"key_status"`
	MissingKeyKIDs      []string                 `json:"missing_key_kids,omitempty"`
	Videos              []TrackInspection        `json:"videos"`
	Audios              []TrackInspection        `json:"audios"`
	Subtitles           []TrackInspection        `json:"subtitles"`
	NativeSupported     bool                     `json:"native_supported"`
	SuggestedEngine     string                   `json:"suggested_engine"`
	CompatibilityReason string                   `json:"compatibility_reason,omitempty"`
	Recommendation      InspectionRecommendation `json:"recommendation"`
	Warnings            []string                 `json:"warnings,omitempty"`
}

func InspectManifest(ctx context.Context, fetcher Fetcher, manifestURL string, preferHeight int, selection config.TrackSelection, keys []config.KeyPair) (ManifestInspection, error) {
	data, finalURL, err := fetcher.Fetch(ctx, manifestURL)
	if err != nil {
		return ManifestInspection{}, err
	}
	presentation, err := mpd.ParseForInspection(data, finalURL)
	if err != nil {
		return ManifestInspection{}, err
	}
	result := ManifestInspection{
		FinalURL:        finalURL,
		Dynamic:         presentation.Dynamic,
		PeriodCount:     len(presentation.Periods),
		SuggestedEngine: EngineFFmpegCopy,
		NativeSupported: false,
	}
	kids := map[string]struct{}{}
	for _, period := range presentation.Periods {
		for _, rep := range period.Representations {
			track := inspectTrack(rep)
			result.Encrypted = result.Encrypted || rep.Encrypted
			if rep.DefaultKID != "" {
				kids[rep.DefaultKID] = struct{}{}
			}
			switch rep.Type {
			case mpd.TypeVideo:
				result.Videos = append(result.Videos, track)
			case mpd.TypeAudio:
				result.Audios = append(result.Audios, track)
			case mpd.TypeText:
				result.Subtitles = append(result.Subtitles, track)
			}
		}
	}
	for kid := range kids {
		result.DefaultKIDs = append(result.DefaultKIDs, kid)
	}
	sort.Strings(result.DefaultKIDs)
	result.KeyStatus = "not_required"
	if result.Encrypted {
		if len(result.DefaultKIDs) == 0 {
			result.KeyStatus = "unknown"
			result.Warnings = append(result.Warnings, "the manifest is encrypted but does not declare a default KID")
		} else {
			configured := make(map[string]struct{}, len(keys))
			for _, key := range keys {
				configured[strings.ToLower(strings.ReplaceAll(key.KID, "-", ""))] = struct{}{}
			}
			for _, kid := range result.DefaultKIDs {
				if _, ok := configured[strings.ToLower(strings.ReplaceAll(kid, "-", ""))]; !ok {
					result.MissingKeyKIDs = append(result.MissingKeyKIDs, kid)
				}
			}
			if len(result.MissingKeyKIDs) == 0 && len(configured) > 0 {
				result.KeyStatus = "matched"
			} else {
				result.KeyStatus = "missing"
				result.Warnings = append(result.Warnings, "one or more manifest KIDs do not have a configured key")
			}
		}
	}
	sortTrackInspections(result.Videos)
	sortTrackInspections(result.Audios)
	sortTrackInspections(result.Subtitles)
	if markAmbiguousTracks(result.Videos, result.Audios, result.Subtitles) {
		result.Warnings = append(result.Warnings, "some tracks have indistinguishable identities and require a more specific custom selector")
	}
	result.Recommendation = recommendTracks(result)

	plan, planErr := PlanFromManifestWithSelection(presentation, preferHeight, selection)
	if planErr != nil {
		result.CompatibilityReason = planErr.Error()
		result.Warnings = append(result.Warnings, planErr.Error())
	} else if plan.Native() {
		result.NativeSupported = true
		result.SuggestedEngine = EngineNativeRewrite
	} else {
		result.CompatibilityReason = plan.Reason
		result.SuggestedEngine = plan.Engine
	}
	if len(presentation.Periods) > 1 {
		result.Warnings = append(result.Warnings, "multi-period manifest requires the compatibility engine")
	}
	return result, nil
}

func inspectTrack(rep mpd.Representation) TrackInspection {
	supported, reason := nativeTrackSupport(rep)
	return TrackInspection{
		Key:               trackIdentity(rep),
		AdaptationSetID:   rep.AdaptationSetID,
		RepresentationID:  rep.ID,
		Type:              string(rep.Type),
		Language:          rep.Lang,
		Roles:             append([]string(nil), rep.Roles...),
		Codec:             rep.Codecs,
		Bandwidth:         rep.Bandwidth,
		Width:             rep.Width,
		Height:            rep.Height,
		FrameRate:         frameRateValue(rep.FrameRate),
		FrameRateRaw:      rep.FrameRate,
		Channels:          rep.AudioChannels,
		SamplingRate:      rep.AudioSamplingRate,
		Encrypted:         rep.Encrypted,
		DefaultKID:        rep.DefaultKID,
		NativeSupported:   supported,
		UnsupportedReason: reason,
	}
}

func nativeTrackSupport(rep mpd.Representation) (bool, string) {
	if rep.UnsupportedReason != "" {
		return false, rep.UnsupportedReason
	}
	if essentialBlocked(rep) {
		return false, "essential property is not supported"
	}
	if !nativeAddressing(rep) {
		return false, ReasonAddressing
	}
	var codecs map[string]struct{}
	switch rep.Type {
	case mpd.TypeVideo:
		if rep.Trick {
			return false, "trick-mode video"
		}
		codecs = nativeVideoCodecs
	case mpd.TypeAudio:
		codecs = nativeAudioCodecs
	case mpd.TypeText:
		codecs = nativeTextCodecs
	default:
		return false, "unknown content type"
	}
	if _, ok := codecs[family(rep.Codecs)]; !ok {
		return false, ReasonManifestCodec
	}
	return true, ""
}

func sortTrackInspections(tracks []TrackInspection) {
	sort.SliceStable(tracks, func(i, j int) bool {
		if tracks[i].Height != tracks[j].Height {
			return tracks[i].Height > tracks[j].Height
		}
		if tracks[i].FrameRate != tracks[j].FrameRate {
			return tracks[i].FrameRate > tracks[j].FrameRate
		}
		if tracks[i].Language != tracks[j].Language {
			return tracks[i].Language < tracks[j].Language
		}
		return tracks[i].Bandwidth > tracks[j].Bandwidth
	})
}

func recommendTracks(result ManifestInspection) InspectionRecommendation {
	recommendation := InspectionRecommendation{}
	for _, video := range result.Videos {
		if video.NativeSupported && !video.Ambiguous {
			recommendation.VideoKey = video.Key
			break
		}
	}
	for _, audio := range result.Audios {
		if audio.NativeSupported && !audio.Ambiguous && containsFold(audio.Roles, "main") {
			recommendation.AudioKey = audio.Key
			break
		}
	}
	if recommendation.AudioKey == "" {
		for _, audio := range result.Audios {
			if audio.NativeSupported && !audio.Ambiguous {
				recommendation.AudioKey = audio.Key
				break
			}
		}
	}
	for _, subtitle := range result.Subtitles {
		if subtitle.NativeSupported && !subtitle.Ambiguous && containsFold(subtitle.Roles, "forced") {
			recommendation.SubtitleKey = subtitle.Key
			break
		}
	}
	return recommendation
}

func markAmbiguousTracks(groups ...[]TrackInspection) bool {
	counts := map[string]int{}
	for _, tracks := range groups {
		for _, track := range tracks {
			counts[track.Key]++
		}
	}
	found := false
	for _, tracks := range groups {
		for i := range tracks {
			if counts[tracks[i].Key] > 1 {
				tracks[i].Ambiguous = true
				found = true
			}
		}
	}
	return found
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}
