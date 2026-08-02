package epg

import (
	"strings"
	"unicode"
)

type MatchStatus string

const (
	MatchMatched   MatchStatus = "matched"
	MatchSuggested MatchStatus = "suggested"
	MatchUnmatched MatchStatus = "unmatched"
)

type ChannelRef struct {
	ID        string `json:"id"`
	Title     string `json:"title,omitempty"`
	LogoURL   string `json:"logo_url,omitempty"`
	EPGID     string `json:"epg_id,omitempty"`
	EPGName   string `json:"epg_name,omitempty"`
	EPGSource string `json:"epg_source,omitempty"`
}

type SourceDocument struct {
	Source   Source
	Document *Document
}

type MatchCandidate struct {
	SourceID  string   `json:"source_id"`
	ChannelID string   `json:"channel_id"`
	Name      string   `json:"name,omitempty"`
	Names     []string `json:"names,omitempty"`
}

type MatchResult struct {
	ChannelID  string           `json:"channel_id"`
	Status     MatchStatus      `json:"status"`
	Match      *MatchCandidate  `json:"match,omitempty"`
	Candidates []MatchCandidate `json:"candidates,omitempty"`
	Logos      []LogoCandidate  `json:"logo_candidates,omitempty"`
}

var knownBroadcastNameFold = map[rune]rune{
	'綫': '线', '線': '线', '臺': '台', '檯': '台',
	'無': '无', '聞': '闻', '視': '视', '頻': '频',
	'華': '华', '衛': '卫', '國': '国', '廣': '广',
	'東': '东', '鳳': '凤', '灣': '湾', '財': '财',
	'經': '经', '濟': '济', '體': '体', '綜': '综',
	'藝': '艺', '兒': '儿', '樂': '乐', '電': '电',
	'劇': '剧', '場': '场', '資': '资', '訊': '讯',
	'粵': '粤', '麗': '丽', '門': '门', '亞': '亚',
	'歐': '欧', '動': '动', '畫': '画', '戲': '戏',
	'寶': '宝', '島': '岛', '馬': '马',
}

var removableBroadcastSuffixes = []string{
	"超高清", "ultrahd", "fullhd", "高清", "uhd", "fhd", "hd", "8k", "4k",
}

func NormalizeName(value string) string {
	var folded strings.Builder
	for _, current := range value {
		switch {
		case current == '\u3000':
			current = ' '
		case current >= '\uff01' && current <= '\uff5e':
			current -= '\ufee0'
		}
		if replacement, ok := knownBroadcastNameFold[current]; ok {
			current = replacement
		}
		if unicode.IsSpace(current) {
			continue
		}
		folded.WriteRune(current)
	}
	normalized := strings.ToLower(folded.String())
	for {
		before := normalized
		for _, suffix := range removableBroadcastSuffixes {
			if strings.HasSuffix(normalized, suffix) {
				normalized = strings.TrimRight(strings.TrimSuffix(normalized, suffix), "-_")
				break
			}
		}
		if normalized == before {
			return normalized
		}
	}
}

func MatchChannel(channel ChannelRef, documents []SourceDocument) MatchResult {
	result := MatchResult{
		ChannelID: channel.ID,
		Status:    MatchUnmatched,
		Logos:     LogoCandidates(firstNonEmpty(channel.EPGName, channel.Title)),
	}
	if channel.EPGID != "" {
		candidates := collectCandidates(documents, channel.EPGSource, func(item Channel) bool {
			return item.ID == channel.EPGID
		})
		if len(candidates) > 0 {
			result.Status = MatchMatched
			result.Match = &candidates[0]
			result.Candidates = candidates
			if len(result.Logos) == 0 {
				result.Logos = LogoCandidates(candidates[0].Name)
			}
			return result
		}
	}

	query := channel.EPGName
	if query == "" {
		query = channel.Title
	}
	normalizedQuery := NormalizeName(query)
	if normalizedQuery == "" {
		return result
	}
	candidates := collectCandidates(documents, channel.EPGSource, func(item Channel) bool {
		for _, name := range item.DisplayNames {
			if NormalizeName(name.Value) == normalizedQuery {
				return true
			}
		}
		return false
	})
	if len(candidates) > 0 {
		result.Status = MatchSuggested
		result.Candidates = candidates
	}
	return result
}

func collectCandidates(documents []SourceDocument, sourceID string, matches func(Channel) bool) []MatchCandidate {
	var candidates []MatchCandidate
	seen := make(map[string]struct{})
	for _, sourceDocument := range documents {
		if sourceDocument.Document == nil || (sourceID != "" && sourceDocument.Source.ID != sourceID) {
			continue
		}
		for _, channel := range sourceDocument.Document.Channels {
			if !matches(channel) {
				continue
			}
			key := sourceDocument.Source.ID + "\x00" + channel.ID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			names := make([]string, 0, len(channel.DisplayNames))
			for _, name := range channel.DisplayNames {
				if name.Value != "" {
					names = append(names, name.Value)
				}
			}
			candidate := MatchCandidate{SourceID: sourceDocument.Source.ID, ChannelID: channel.ID, Names: names}
			if len(names) > 0 {
				candidate.Name = names[0]
			}
			candidates = append(candidates, candidate)
		}
	}
	return candidates
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
