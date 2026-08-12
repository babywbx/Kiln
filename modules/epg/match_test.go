package epg_test

import (
	"context"
	"testing"

	"github.com/babywbx/kiln/modules/epg"
)

func TestNormalizeNameFoldsKnownBroadcastVariantsWidthAndSuffixes(t *testing.T) {
	t.Parallel()

	want := "无线新闻台"
	for _, input := range []string{"無綫新聞台", "無線新聞台 高清", "無　綫新聞台 ＨＤ"} {
		if got := epg.NormalizeName(input); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", input, got, want)
		}
	}
	if got := epg.NormalizeName("翡翠臺 ４Ｋ"); got != "翡翠台" {
		t.Errorf("NormalizeName() = %q, want 翡翠台", got)
	}
}

func TestMatchChannelUsesIDBeforeName(t *testing.T) {
	t.Parallel()

	result := matchSample(t, epg.ChannelRef{ID: "kiln-news", EPGID: "368359", EPGName: "TVBS新聞台"})
	if result.Status != epg.MatchMatched || result.Match == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Match.ChannelID != "368359" || result.Match.SourceID != "hk" {
		t.Fatalf("match = %+v", result.Match)
	}
}

func TestMatchChannelReturnsAllDuplicateNameCandidates(t *testing.T) {
	t.Parallel()

	result := matchSample(t, epg.ChannelRef{ID: "kiln-demo", EPGName: "ＴＶＢＳ 新聞台 HD"})
	if result.Status != epg.MatchSuggested {
		t.Fatalf("status = %q, want suggested", result.Status)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want two", result.Candidates)
	}
	if result.Candidates[0].ChannelID != "456556" || result.Candidates[1].ChannelID != "492811" {
		t.Fatalf("candidates = %+v", result.Candidates)
	}
}

func TestMatchChannelHonorsSourceSelection(t *testing.T) {
	t.Parallel()

	result := matchSample(t, epg.ChannelRef{ID: "kiln-news", EPGID: "368359", EPGSource: "tw"})
	if result.Status != epg.MatchUnmatched {
		t.Fatalf("result = %+v, want unmatched", result)
	}
}

func matchSample(t *testing.T, channel epg.ChannelRef) epg.MatchResult {
	t.Helper()
	service := epg.NewService(epg.ServiceConfig{
		Sources: []epg.Source{
			{ID: "hk", Timezone: "Asia/Hong_Kong"},
			{ID: "tw", Timezone: "Asia/Taipei"},
		},
	}, &fakeSourceFetcher{results: map[string]epg.FetchResult{
		"hk": {Data: []byte(`<tv><channel id="368359"><display-name>無綫新聞台</display-name></channel></tv>`)},
		"tw": {Data: []byte(`<tv><channel id="456556"><display-name>TVBS 新聞台</display-name></channel>` +
			`<channel id="492811"><display-name>TVBS新聞台 HD</display-name></channel></tv>`)},
	}}, newTestStore(t))
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	matches := service.Matches([]epg.ChannelRef{channel})
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want one result", matches)
	}
	return matches[0]
}
