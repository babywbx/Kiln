package pull

import (
	"net/http"
	"net/url"
	"testing"
)

func TestUpgradeInsecureURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      string
		want    string
		changed bool
	}{
		{"plain http", "http://cdn.example.com/a.mpd?sig=1", "https://cdn.example.com/a.mpd?sig=1", true},
		{"explicit port 80", "http://cdn.example.com:80/a.mpd", "https://cdn.example.com/a.mpd", true},
		{"already https", "https://cdn.example.com/a.mpd", "https://cdn.example.com/a.mpd", false},
		{"non default port", "http://cdn.example.com:8080/a.mpd", "http://cdn.example.com:8080/a.mpd", false},
		{"loopback", "http://127.0.0.1/a.mpd", "http://127.0.0.1/a.mpd", false},
		{"localhost", "http://localhost/a.mpd", "http://localhost/a.mpd", false},
		{"localhost trailing dot", "http://localhost./a.mpd", "http://localhost./a.mpd", false},
		{"local dns", "http://origin.local/a.mpd", "http://origin.local/a.mpd", false},
		{"private", "http://10.10.5.60/a.mpd", "http://10.10.5.60/a.mpd", false},
		{"carrier grade nat", "http://100.64.0.1/a.mpd", "http://100.64.0.1/a.mpd", false},
		{"multicast", "http://224.0.0.1/a.mpd", "http://224.0.0.1/a.mpd", false},
		{"single label host", "http://cdn/a.mpd", "https://cdn/a.mpd", true},
		{"ipv6", "http://[2001:db8::1]/a.mpd", "https://[2001:db8::1]/a.mpd", true},
		{"ipv6 zone", "http://[fe80::1%25en0]/a.mpd", "http://[fe80::1%25en0]/a.mpd", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u, err := url.Parse(tc.in)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.in, err)
			}
			if got := upgradeInsecureURL(u, nil); got != tc.changed {
				t.Fatalf("changed = %v, want %v", got, tc.changed)
			}
			if got := u.String(); got != tc.want {
				t.Fatalf("url = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUpgradeInsecureURLNil(t *testing.T) {
	if upgradeInsecureURL(nil, nil) {
		t.Fatal("nil url reported as upgraded")
	}
}

func TestUpgradeInsecureURLSkipsExplicitPrivateHostname(t *testing.T) {
	u, err := url.Parse("http://origin.example/a.mpd")
	if err != nil {
		t.Fatal(err)
	}
	if upgradeInsecureURL(u, map[string]struct{}{"origin.example": {}}) {
		t.Fatal("explicit private hostname was upgraded")
	}
}

func TestStopRedirectReturnsUpgradedLocation(t *testing.T) {
	next, err := http.NewRequest(http.MethodGet, "http://cdn.example.com/next.mpd", nil)
	if err != nil {
		t.Fatal(err)
	}
	next.Response = &http.Response{Header: http.Header{"Location": {"http://cdn.example.com/next.mpd"}}}
	client := New(Options{}).pinnedClient("", nil, true, true)
	if err := client.CheckRedirect(next, []*http.Request{new(http.Request)}); err != http.ErrUseLastResponse {
		t.Fatalf("redirect error = %v, want ErrUseLastResponse", err)
	}
	if got := next.Response.Header.Get("Location"); got != "https://cdn.example.com/next.mpd" {
		t.Fatalf("Location = %q", got)
	}
}
