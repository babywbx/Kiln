package epg

import (
	"net/url"
	"strings"
)

const (
	logoSourcePrimary       = "logo-primary"
	logoSourceSecondary = "logo-secondary"
	logoSourceTertiary     = "logo-tertiary"
)

type LogoCandidate struct {
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Priority int    `json:"priority"`
}

type knownLogoFile struct {
	sourceID string
	baseURL  string
	path     string
	priority int
}

var knownLogoAliases = map[string]string{
	"无线新闻": "wireless-news", "无线新闻台": "wireless-news", "tvb无线新闻台": "wireless-news",
	"翡翠台": "jade", "tvb翡翠台": "jade", "tvbjade": "jade",
	"tvbs新闻": "tvbs-news", "tvbs新闻台": "tvbs-news", "tvbsnews": "tvbs-news",
}

var knownLogoFiles = map[string][]knownLogoFile{
	"wireless-news": {
		{logoSourcePrimary, "https://gitee.com/mytv-android/myTVlogo/raw/main/", "img/無線新聞台.png", 1},
		{logoSourceSecondary, "https://raw.githubusercontent.com/fanmingming/live/main/", "tv/无线新闻台.png", 2},
		{logoSourceTertiary, "https://raw.githubusercontent.com/iptv-pro/iptv-pro.github.io/main/", "logo/TVB无线新闻台png.png", 3},
	},
	"jade": {
		{logoSourcePrimary, "https://gitee.com/mytv-android/myTVlogo/raw/main/", "img/翡翠台.png", 1},
		{logoSourceSecondary, "https://raw.githubusercontent.com/fanmingming/live/main/", "tv/TVB翡翠台.png", 2},
		{logoSourceSecondary, "https://raw.githubusercontent.com/fanmingming/live/main/", "tv/翡翠台.png", 2},
		{logoSourceTertiary, "https://raw.githubusercontent.com/iptv-pro/iptv-pro.github.io/main/", "logo/翡翠台.png", 3},
		{logoSourceTertiary, "https://raw.githubusercontent.com/iptv-pro/iptv-pro.github.io/main/", "logo/TVB Jade.png", 3},
	},
	"tvbs-news": {
		{logoSourcePrimary, "https://gitee.com/mytv-android/myTVlogo/raw/main/", "img/TVBS新聞台.png", 1},
		{logoSourceSecondary, "https://raw.githubusercontent.com/fanmingming/live/main/", "tv/TVBS新闻.png", 2},
		{logoSourceTertiary, "https://raw.githubusercontent.com/iptv-pro/iptv-pro.github.io/main/", "logo/TVBS News.png", 3},
		{logoSourceTertiary, "https://raw.githubusercontent.com/iptv-pro/iptv-pro.github.io/main/", "logo/TVBS新闻.png", 3},
	},
}

// LogoCandidates returns stable, pre-verified URL candidates without network
// probing. Known aliases use the same normalization as EPG name matching.
func LogoCandidates(name string) []LogoCandidate {
	key, ok := knownLogoAliases[NormalizeName(name)]
	if !ok {
		return nil
	}
	files := knownLogoFiles[key]
	candidates := make([]LogoCandidate, 0, len(files))
	seen := make(map[string]struct{}, len(files))
	for _, file := range files {
		candidateURL := file.baseURL + escapeLogoPath(file.path)
		if _, ok := seen[candidateURL]; ok {
			continue
		}
		seen[candidateURL] = struct{}{}
		candidates = append(candidates, LogoCandidate{
			SourceID: file.sourceID, Name: file.path, URL: candidateURL, Priority: file.priority,
		})
	}
	return candidates
}

func escapeLogoPath(value string) string {
	parts := strings.Split(value, "/")
	for index := range parts {
		parts[index] = url.PathEscape(parts[index])
	}
	return strings.Join(parts, "/")
}
