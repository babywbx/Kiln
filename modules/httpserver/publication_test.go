//go:build !lite

package httpserver_test

import (
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/playback"
)

// A master playlist references its renditions through URI="..." attributes, and
// EXT-X-MAP does too. Rewriting only bare lines would leave the audio playlist
// and the init segment pointing at names this server never serves.
func TestRewriteLocalPlaylistCoversTagURIs(t *testing.T) {
	in := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="audio-main",URI="audio-main.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=120000,CODECS="hvc1.1.6.L60.90,mp4a.40.2",AUDIO="audio"
video-main.m3u8
`
	out := string(playback.RewriteLocalPlaylist([]byte(in), "/v1/play/demo/live/", "t0k", "gen1"))

	for _, want := range []string{
		`URI="/v1/play/demo/live/audio-main.m3u8?g=gen1&token=t0k"`,
		"/v1/play/demo/live/video-main.m3u8?g=gen1&token=t0k",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, `URI="audio-main.m3u8"`) {
		t.Errorf("audio rendition URI was left unrewritten:\n%s", out)
	}
}

func TestRewriteLocalPlaylistRewritesInitSegment(t *testing.T) {
	in := `#EXTM3U
#EXT-X-MAP:URI="video-main-init.mp4"
#EXTINF:2.000,
video-main-000001.m4s
`
	out := string(playback.RewriteLocalPlaylist([]byte(in), "/v1/play/demo/live/", "", "gen1"))
	for _, want := range []string{
		`#EXT-X-MAP:URI="/v1/play/demo/live/video-main-init.mp4?g=gen1"`,
		"/v1/play/demo/live/video-main-000001.m4s?g=gen1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// A path token already authenticates the URL; repeating it as a query parameter
// would only widen where the token appears.
func TestRewriteLocalPlaylistOmitsQueryTokenForPathTokens(t *testing.T) {
	in := "#EXT-X-MAP:URI=\"video-main-init.mp4\"\nvideo-main-000001.m4s\n"
	out := string(playback.RewriteLocalPlaylist([]byte(in), "/p/abc/play/demo/live/", "t0k", "gen1"))

	if strings.Contains(out, "token=") {
		t.Errorf("path-token playlist should carry no query token:\n%s", out)
	}
	if !strings.Contains(out, `URI="/p/abc/play/demo/live/video-main-init.mp4?g=gen1"`) {
		t.Errorf("init segment was not rewritten onto the path-token prefix:\n%s", out)
	}
}

// A published playlist should only ever name plain files. Anything else is left
// alone rather than turned into a URL on this server.
func TestRewriteLocalPlaylistRejectsTraversal(t *testing.T) {
	in := "../../etc/passwd\nhttps://evil.example.com/seg.m4s\n"
	out := string(playback.RewriteLocalPlaylist([]byte(in), "/v1/play/demo/live/", "", "gen1"))

	if strings.Contains(out, "/v1/play/demo/live/passwd") {
		t.Errorf("traversal was rewritten into a servable URL:\n%s", out)
	}
	if strings.Contains(out, "/v1/play/demo/live/seg.m4s") {
		t.Errorf("an absolute foreign URL was rewritten as a local asset:\n%s", out)
	}
}
