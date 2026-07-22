//go:build !lite

package proxyegress

import (
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func TestEmptyStoreDoesNotFallBackToFileEgress(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SeedFromConfig(config.File{Egress: config.Egress{Default: Direct, PlaylistPolicy: string(PolicyRewrite)}}); err != nil {
		t.Fatal(err)
	}
	cfg, err := ConfigFromStore(db, config.File{
		Proxies: []config.ProxyProfile{{ID: "file-proxy", URL: "http://127.0.0.1:8080"}},
		Egress:  config.Egress{Default: "file-proxy", Rules: []config.EgressRule{{ID: "file-rule", Proxy: "file-proxy"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 || len(cfg.Rules) != 0 {
		t.Fatalf("empty authoritative store fell back to file: %#v", cfg)
	}
}
