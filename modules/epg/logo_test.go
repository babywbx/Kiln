package epg_test

import (
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

func TestLogoCandidatesNormalizeNamesAndKeepSourcePriority(t *testing.T) {
	t.Parallel()

	candidates := epg.LogoCandidates("無綫新聞台 HD")
	if len(candidates) != 3 {
		t.Fatalf("candidates = %+v, want three", candidates)
	}
	wantSources := []string{"logo-primary", "logo-secondary", "logo-tertiary"}
	for index, want := range wantSources {
		if candidates[index].SourceID != want {
			t.Errorf("candidate %d source = %q, want %q", index, candidates[index].SourceID, want)
		}
		if strings.ContainsAny(candidates[index].URL, "無綫線新聞台 ") {
			t.Errorf("candidate URL was not path escaped: %q", candidates[index].URL)
		}
	}
	if !strings.Contains(candidates[2].URL, "TVB%E6%97%A0%E7%BA%BF%E6%96%B0%E9%97%BB%E5%8F%B0png.png") {
		t.Fatalf("unexpected iptv-pro URL: %q", candidates[2].URL)
	}
}

func TestLogoCandidatesIncludeConfirmedAliasesAndEscapeSpaces(t *testing.T) {
	t.Parallel()

	jade := epg.LogoCandidates("TVB 翡翠臺 4K")
	if len(jade) != 5 {
		t.Fatalf("jade candidates = %+v, want five confirmed files", jade)
	}
	if !strings.Contains(jade[4].URL, "TVB%20Jade.png") {
		t.Fatalf("space was not PathEscaped: %q", jade[4].URL)
	}

	tvbs := epg.LogoCandidates("TVBS新聞台")
	if len(tvbs) != 4 {
		t.Fatalf("TVBS candidates = %+v, want four confirmed files", tvbs)
	}
	if !strings.Contains(tvbs[2].URL, "TVBS%20News.png") {
		t.Fatalf("space was not PathEscaped: %q", tvbs[2].URL)
	}
}
