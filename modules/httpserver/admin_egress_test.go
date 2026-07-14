package httpserver

import (
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/store"
)

func TestNormalizeEgressDraftRejectsDisabledProxyReferences(t *testing.T) {
	tests := []struct {
		name  string
		draft egressDraft
		want  string
	}{
		{
			name: "default",
			draft: egressDraft{
				Default: "proxy", PlaylistPolicy: "rewrite",
				Proxies: []store.ProxyProfileRow{{ID: "proxy", URL: "http://127.0.0.1:8080", Disabled: true}},
			},
			want: "default proxy",
		},
		{
			name: "enabled rule",
			draft: egressDraft{
				Default: "direct", PlaylistPolicy: "rewrite",
				Proxies: []store.ProxyProfileRow{{ID: "proxy", URL: "http://127.0.0.1:8080", Disabled: true}},
				Rules:   []store.ProxyRuleRow{{ID: "rule", Kind: "host_suffix", Pattern: "example.com", ProxyID: "proxy"}},
			},
			want: "references disabled proxy",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := normalizeEgressDraft(test.draft, nil); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
