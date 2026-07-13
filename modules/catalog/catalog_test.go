package catalog

import (
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/config"
)

func TestM3UAdvertisesRewrittenEPG(t *testing.T) {
	svc := New(config.File{}, nil)
	body := svc.M3U([]config.Channel{{
		ID: "channel-news", Title: "News Channel", Group: "News",
		LogoURL: "https://logo.example/news.png", EPGID: "source-news", EPGName: "News Channel",
	}}, "https://kiln.example", "/v1/play/", "token", "https://kiln.example/v1/epg.xml.gz")

	wants := []string{
		`#EXTM3U x-tvg-url="https://kiln.example/v1/epg.xml.gz"`,
		`tvg-id="demo-news"`,
		`tvg-name="News Channel"`,
		`tvg-logo="https://logo.example/news.png"`,
		`group-title="News"`,
	}
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Fatalf("M3U missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `tvg-id="source-news"`) {
		t.Fatalf("M3U leaked source EPG id instead of Kiln id:\n%s", body)
	}
}
