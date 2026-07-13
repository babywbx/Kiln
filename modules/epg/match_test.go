package epg_test

import (
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

	documents := sampleSourceDocuments()
	result := epg.MatchChannel(epg.ChannelRef{
		ID: "kiln-news", EPGID: "368359", EPGName: "TVBS新聞台",
	}, documents)
	if result.Status != epg.MatchMatched || result.Match == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Match.ChannelID != "368359" || result.Match.SourceID != "hk" {
		t.Fatalf("match = %+v", result.Match)
	}
}

func TestMatchChannelReturnsAllDuplicateNameCandidates(t *testing.T) {
	t.Parallel()

	result := epg.MatchChannel(epg.ChannelRef{
		ID: "kiln-demo", EPGName: "ＴＶＢＳ 新聞台 HD",
	}, sampleSourceDocuments())
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

	result := epg.MatchChannel(epg.ChannelRef{
		ID: "kiln-news", EPGID: "368359", EPGSource: "tw",
	}, sampleSourceDocuments())
	if result.Status != epg.MatchUnmatched {
		t.Fatalf("result = %+v, want unmatched", result)
	}
}

func sampleSourceDocuments() []epg.SourceDocument {
	return []epg.SourceDocument{
		{
			Source: epg.Source{ID: "hk"},
			Document: &epg.Document{Channels: []epg.Channel{
				{ID: "368359", DisplayNames: []epg.Text{{Value: "無綫新聞台"}}},
			}},
		},
		{
			Source: epg.Source{ID: "tw"},
			Document: &epg.Document{Channels: []epg.Channel{
				{ID: "456556", DisplayNames: []epg.Text{{Value: "TVBS 新聞台"}}},
				{ID: "492811", DisplayNames: []epg.Text{{Value: "TVBS新聞台 HD"}}},
			}},
		},
	}
}
