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
	}, func(string) string { return "signed" })
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
	out, err := RewritePlaylist(
		in, "http://origin.example/live/index.m3u8", "/v1/play/ch/u/", nil,
		func(string) bool { return true },
		func(string) string { return "signed" },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, "/v1/play/ch/u/") {
		t.Fatalf("expected rewrite all: %s", out)
	}
}

func TestRewritePlaylistFailsClosedForForbiddenTargets(t *testing.T) {
	tests := []struct {
		name   string
		target string
		line   string
	}{
		{"private media", "http://10.0.0.1/private.ts", "http://10.0.0.1/private.ts"},
		{"loopback tag", "http://127.0.0.1/key", `#EXT-X-KEY:METHOD=AES-128,URI="http://127.0.0.1/key"`},
		{"loopback hostname", "http://localhost.:1/private.ts", "http://localhost.:1/private.ts"},
		{"metadata media", "http://169.254.169.254/latest/meta-data", "http://169.254.169.254/latest/meta-data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out, err := RewritePlaylist(
				"#EXTM3U\n"+test.line+"\n", "https://origin.example/live/index.m3u8",
				"/v1/play/ch/u/", nil, func(string) bool { return true }, func(string) string { return "signed" },
			)
			if err == nil {
				t.Fatal("expected forbidden target error")
			}
			if contains(out, test.target) {
				t.Fatalf("forbidden target leaked in output: %s", out)
			}
		})
	}
}

func TestRewritePlaylistAllowsExplicitPrivateTarget(t *testing.T) {
	for _, test := range []struct {
		target string
		host   string
	}{
		{"http://10.0.0.1/private.ts", "10.0.0.1"},
		{"http://localhost./private.ts", "localhost"},
	} {
		out, err := RewritePlaylist(
			"#EXTM3U\n"+test.target+"\n", "https://origin.example/live/index.m3u8", "/v1/play/ch/u/",
			map[string]struct{}{test.host: {}}, func(string) bool { return true }, func(string) string { return "signed" },
		)
		if err != nil {
			t.Fatal(err)
		}
		if !contains(out, "/v1/play/ch/u/") || contains(out, test.target) {
			t.Fatalf("explicit private target was not rewritten: %s", out)
		}
	}
}

func TestRewritePlaylistPassthroughKeepsForbiddenTarget(t *testing.T) {
	const target = "http://127.0.0.1/private.ts"
	out, err := RewritePlaylist(
		"#EXTM3U\n"+target+"\n", "https://origin.example/live/index.m3u8", "/v1/play/ch/u/",
		nil, func(string) bool { return false }, func(string) string { return "signed" },
	)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(out, target) {
		t.Fatalf("passthrough target changed: %s", out)
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

func TestUpstreamRoundTripKeepsMediaExtension(t *testing.T) {
	tests := []struct {
		absolute string
		suffix   string
	}{
		{"https://cdn.example/live/seg0.ts", ".ts"},
		{"https://cdn.example/live/seg0.m4s?token=abc", ".m4s"},
		{"https://cdn.example/live/master.M3U8", ".m3u8"},
		{"https://cdn.example/live/stream", ""},
		{"https://cdn.example/live.dir/segment", ""},
		{"https://cdn.example/live/seg0.bin", ""},
	}
	for _, test := range tests {
		encoded := EncodeUpstream(test.absolute)
		if test.suffix != "" && !hasSuffix(encoded, test.suffix) {
			t.Fatalf("EncodeUpstream(%q) = %q, want suffix %q", test.absolute, encoded, test.suffix)
		}
		decoded, err := DecodeUpstream(encoded)
		if err != nil {
			t.Fatalf("DecodeUpstream(%q) error: %v", encoded, err)
		}
		if decoded != test.absolute {
			t.Fatalf("round trip = %q, want %q", decoded, test.absolute)
		}
	}
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
