package httpserver

import (
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/accesstoken"
)

func TestRedactRequestPathRemovesDistributionToken(t *testing.T) {
	token, err := accesstoken.Generate()
	if err != nil {
		t.Fatal(err)
	}
	raw := "/p/" + token + "/play/news/index.m3u8"
	got := redactRequestPath(raw)
	if strings.Contains(got, token) {
		t.Fatalf("redacted path leaked token: %q", got)
	}
	want := "/p/" + accesstoken.Prefix(token) + "…/play/news/index.m3u8"
	if got != want {
		t.Fatalf("redacted path = %q, want %q", got, want)
	}
}

func TestRedactRequestPathLeavesOtherPathsAlone(t *testing.T) {
	const path = "/v1/admin/channels/news"
	if got := redactRequestPath(path); got != path {
		t.Fatalf("path = %q", got)
	}
}

func TestPublicURLRemovesCredentialsAndQuery(t *testing.T) {
	got := publicURL("https://user:secret@example.com/live/index.m3u8?token=abc&quality=4k", false)
	if got != "https://example.com/live/index.m3u8" {
		t.Fatalf("public url = %q", got)
	}
	host := publicURL("socks5://user:secret@127.0.0.1:6153", true)
	if host != "socks5://127.0.0.1:6153" {
		t.Fatalf("public proxy url = %q", host)
	}
}
