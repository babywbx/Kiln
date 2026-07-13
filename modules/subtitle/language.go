package subtitle

import "strings"

// NormalizeLanguage maps the language labels used by the upstream subtitle
// tracks to unambiguous HLS-compatible BCP 47 tags. In this source, bare zh is
// the traditional track while chs is the simplified track.
func NormalizeLanguage(raw string) Language {
	tag := canonicalLanguageTag(strings.TrimSpace(raw))
	switch strings.ToLower(tag) {
	case "zh", "zht", "cht", "zh-hant", "zh-tw", "zh-hk", "zh-mo":
		return Language{Tag: "zh-Hant", Name: "繁體中文"}
	case "chs", "zhs", "zh-hans", "zh-cn", "zh-sg":
		return Language{Tag: "zh-Hans", Name: "简体中文"}
	case "en", "eng":
		return Language{Tag: "en", Name: "English"}
	case "":
		return Language{Tag: "und", Name: "Unknown"}
	}
	if strings.HasPrefix(strings.ToLower(tag), "en-") {
		return Language{Tag: tag, Name: "English"}
	}
	return Language{Tag: tag, Name: tag}
}

func canonicalLanguageTag(raw string) string {
	parts := strings.Split(strings.ReplaceAll(raw, "_", "-"), "-")
	for index, part := range parts {
		switch {
		case index == 0:
			parts[index] = strings.ToLower(part)
		case len(part) == 4:
			lower := strings.ToLower(part)
			parts[index] = strings.ToUpper(lower[:1]) + lower[1:]
		case len(part) == 2 || len(part) == 3:
			parts[index] = strings.ToUpper(part)
		default:
			parts[index] = strings.ToLower(part)
		}
	}
	return strings.Join(parts, "-")
}
