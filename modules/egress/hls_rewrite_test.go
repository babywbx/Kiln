package egress

import "testing"

func TestRewritePlaylistProxiesWhenDecisionSaysSo(t *testing.T) {
	in := `#EXTM3U
#EXTINF:2.0,
seg0.ts
#EXTINF:2.0,
https://cdn.example/x.ts
`
	allowed := map[string]struct{}{"origin.example": {}}
	out, err := RewritePlaylist(in, "http://origin.example/live/index.m3u8", "/v1/play/ch/u/", allowed, func(abs string) bool {
		return !contains(abs, "cdn.example")
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "/v1/play/ch/u/") {
		t.Fatalf("expected proxy rewrite: %s", out)
	}
	if !contains(out, "https://cdn.example/x.ts") {
		t.Fatalf("expected external host left intact: %s", out)
	}
}

func TestRewritePlaylistRewriteAllPublic(t *testing.T) {
	in := `#EXTM3U
https://cdn.example/x.ts
`
	out, err := RewritePlaylist(in, "http://origin.example/live/index.m3u8", "/v1/play/ch/u/", nil, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "/v1/play/ch/u/") {
		t.Fatalf("expected rewrite all: %s", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
