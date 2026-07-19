package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/store"
)

func TestBuildEPGServiceUsesPersistedSourcesWithoutLegacyEnabledFlag(t *testing.T) {
	directory := t.TempDir()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertEPGSource(store.EPGSourceRow{
		ID: "fixture", URL: "https://example.test/epg.xml", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	service, err := buildEPGService(config.File{
		Server: config.Server{DataDir: directory},
		EPG:    config.EPG{Enabled: false},
	}, db, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if service == nil || len(service.Sources()) != 1 || service.Sources()[0].ID != "fixture" {
		t.Fatalf("service sources = %+v", service)
	}
	if info, err := os.Stat(filepath.Join(directory, "epg")); err != nil || !info.IsDir() {
		t.Fatalf("default EPG cache directory was not created: info=%v err=%v", info, err)
	}
}

func TestGoMemoryLimitMBReportsRuntimeDefaultAsUnlimited(t *testing.T) {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if got := goMemoryLimitMB(maxInt64); got != 0 {
		t.Fatalf("default Go memory limit = %d MiB, want 0", got)
	}
	if got := goMemoryLimitMB(768 << 20); got != 768 {
		t.Fatalf("explicit Go memory limit = %d MiB, want 768", got)
	}
}
