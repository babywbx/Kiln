package catalog

import "testing"

func TestParseAndMapStreamPlaylist(t *testing.T) {
	raw := `#EXTM3U
#EXTINF:1 tvg-id="source-news" tvg-name="News Channel" tvg-logo="http://logo/a.png" group-title="News",News Channel
http://origin.example.com:8080/stream/demo/main/master.m3u8?u=demo&p=demo
#EXTINF:2 group-title="Entertainment",UHD Channel
http://origin.example.com:8080/stream/demo/news/master.m3u8?u=demo&p=demo
`
	entries := ParseM3U(raw)
	if len(entries) != 2 {
		t.Fatalf("entries=%d", len(entries))
	}
	if entries[0].Title != "News Channel" || entries[0].Group != "News" {
		t.Fatalf("%+v", entries[0])
	}
	if entries[0].TvgID != "source-news" || entries[0].TvgName != "News Channel" {
		t.Fatalf("epg metadata not preserved: %+v", entries[0])
	}
	sug := SuggestImport(entries, ImportOptions{DefaultUpstream: "origin"})
	if sug[0].SuggestedPath != "/live/null" || sug[0].SuggestedIngress != "dash" {
		t.Fatalf("tv map %+v", sug[0])
	}
	if sug[1].SuggestedPath != "/vod/news" || sug[1].SuggestedIngress != "hls" {
		t.Fatalf("vod map %+v", sug[1])
	}
	if sug[0].SuggestedID == "" || sug[1].SuggestedID == "" {
		t.Fatal("ids")
	}
}
