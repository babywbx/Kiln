package packager

import "strings"

func preferredAudioIndex(languages, preferences []string) int {
	for _, preference := range preferences {
		want := normalizeLanguage(preference)
		if want == "" {
			continue
		}
		for i, language := range languages {
			if languageMatches(normalizeLanguage(language), want) {
				return i
			}
		}
	}
	return 0
}

func languageMatches(language, preference string) bool {
	if language == preference {
		return true
	}
	return preference == "zh" && (language == "zh-hans" || language == "zh-hant")
}

func normalizeLanguage(language string) string {
	language = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
	switch language {
	case "chi", "zho":
		return "zh"
	case "chs", "zh-cn", "zh-sg", "zh-hans":
		return "zh-hans"
	case "cht", "zh-tw", "zh-hk", "zh-mo", "zh-hant":
		return "zh-hant"
	case "eng":
		return "en"
	case "cantonese", "zh-yue":
		return "yue"
	}
	return language
}
