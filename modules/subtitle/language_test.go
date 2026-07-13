package subtitle

import "testing"

func TestNormalizeLanguageDistinguishesTraditionalSimplifiedAndEnglishTracks(t *testing.T) {
	t.Parallel()

	tests := map[string]Language{
		"zh":    {Tag: "zh-Hant", Name: "繁體中文"},
		" ZH ":  {Tag: "zh-Hant", Name: "繁體中文"},
		"chs":   {Tag: "zh-Hans", Name: "简体中文"},
		"zh-CN": {Tag: "zh-Hans", Name: "简体中文"},
		"zh-HK": {Tag: "zh-Hant", Name: "繁體中文"},
		"en":    {Tag: "en", Name: "English"},
		"eng":   {Tag: "en", Name: "English"},
	}

	for raw, want := range tests {
		raw, want := raw, want
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			if got := NormalizeLanguage(raw); got != want {
				t.Fatalf("NormalizeLanguage(%q) = %#v, want %#v", raw, got, want)
			}
		})
	}
}

func TestNormalizeLanguageKeepsUnknownLanguageUsable(t *testing.T) {
	t.Parallel()

	if got, want := NormalizeLanguage("fr-CA"), (Language{Tag: "fr-CA", Name: "fr-CA"}); got != want {
		t.Fatalf("NormalizeLanguage(fr-CA) = %#v, want %#v", got, want)
	}
	if got, want := NormalizeLanguage(""), (Language{Tag: "und", Name: "Unknown"}); got != want {
		t.Fatalf("NormalizeLanguage(empty) = %#v, want %#v", got, want)
	}
}
