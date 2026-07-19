package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/proxyegress"
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

func TestBuildEPGServiceNewInstallHasNoActiveSources(t *testing.T) {
	directory := t.TempDir()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	router, err := proxyegress.NewRouter(proxyegress.Config{})
	if err != nil {
		t.Fatal(err)
	}

	service, err := buildEPGService(config.File{
		Server: config.Server{DataDir: directory},
	}, db, router, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if sources := service.Sources(); len(sources) != 0 {
		t.Fatalf("new install active EPG sources = %+v, want none", sources)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("empty EPG refresh = %v", err)
	}
}

func TestBuildEPGServiceUsesDirectByDefaultAndSelectedProxy(t *testing.T) {
	const guide = `<tv><channel id="one"><display-name>One</display-name></channel></tv>`
	var originRequests atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originRequests.Add(1)
		_, _ = io.WriteString(w, guide)
	}))
	t.Cleanup(origin.Close)

	var proxyRequests atomic.Int64
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		_, _ = io.WriteString(w, guide)
	}))
	t.Cleanup(proxy.Close)
	router, err := proxyegress.NewRouter(proxyegress.Config{
		Default: "selected",
		Profiles: []proxyegress.Profile{{
			ID: "selected", URL: proxy.URL,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	db, err := store.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertEPGSource(store.EPGSourceRow{
		ID: "custom", URL: origin.URL, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	cache := false
	cfg := config.File{Server: config.Server{DataDir: directory}, EPG: config.EPG{Cache: &cache}}

	service, err := buildEPGService(cfg, db, router, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if originRequests.Load() != 1 || proxyRequests.Load() != 0 {
		t.Fatalf("default EPG egress used origin=%d proxy=%d, want direct", originRequests.Load(), proxyRequests.Load())
	}

	rows, err := db.ListEPGSources()
	if err != nil || len(rows) != 1 {
		t.Fatalf("EPG source rows = %#v, %v", rows, err)
	}
	rows[0].Proxy = "selected"
	if err := db.UpsertEPGSourceIfRevision(rows[0], rows[0].Revision); err != nil {
		t.Fatal(err)
	}
	service, err = buildEPGService(cfg, db, router, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if originRequests.Load() != 1 || proxyRequests.Load() != 1 {
		t.Fatalf("selected EPG egress used origin=%d proxy=%d, want proxy", originRequests.Load(), proxyRequests.Load())
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
