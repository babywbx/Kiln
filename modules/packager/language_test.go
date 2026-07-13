package packager

import "testing"

func TestPreferredAudioLanguageUsesOrderedAliases(t *testing.T) {
	tests := []struct {
		name        string
		languages   []string
		preferences []string
		want        int
	}{
		{name: "cantonese first", languages: []string{"zho", "yue", "eng"}, preferences: []string{"zh-yue", "zh"}, want: 1},
		{name: "english alias", languages: []string{"zho", "eng"}, preferences: []string{"en"}, want: 1},
		{name: "traditional family", languages: []string{"chs", "cht"}, preferences: []string{"zh-Hant"}, want: 1},
		{name: "fallback first", languages: []string{"zho", "eng"}, preferences: []string{"ja"}, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := preferredAudioIndex(test.languages, test.preferences); got != test.want {
				t.Fatalf("preferredAudioIndex(%v, %v) = %d, want %d", test.languages, test.preferences, got, test.want)
			}
		})
	}
}
