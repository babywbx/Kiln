//go:build !lite

package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/babywbx/kiln/modules/playback"
)

func TestParseHLSPlaylistRequestSupportsBlockingReloadAndDelta(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/live/video.m3u8?_HLS_msn=12&_HLS_part=3&_HLS_skip=v2", nil)
	parsed, lowLatency, err := playback.ParseHLSPlaylistRequest(request)
	if err != nil {
		t.Fatalf("parseHLSPlaylistRequest: %v", err)
	}
	if !lowLatency || !parsed.Skip || parsed.MSN == nil || *parsed.MSN != 12 || parsed.Part == nil || *parsed.Part != 3 {
		t.Fatalf("parsed request = %#v, low latency = %v", parsed, lowLatency)
	}
}

func TestParseHLSPlaylistRequestRejectsPartWithoutMSN(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest(http.MethodGet, "/live/video.m3u8?_HLS_part=0", nil)
	if _, _, err := playback.ParseHLSPlaylistRequest(request); err == nil {
		t.Fatal("part without MSN was accepted")
	}
}
