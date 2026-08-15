//go:build !lite

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/resources"
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
	}, db, nil, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
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
	t.Cleanup(func() { _ = service.Close() })
	if sources := service.Sources(); len(sources) != 0 {
		t.Fatalf("new install active EPG sources = %+v, want none", sources)
	}
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("empty EPG refresh = %v", err)
	}
}

func TestBuildEPGServiceUsesDirectByDefaultAndRejectsUnpinnableProxy(t *testing.T) {
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
	cfg := config.File{
		Server:   config.Server{DataDir: directory},
		EPG:      config.EPG{Cache: &cache},
		Security: config.Security{AllowedHosts: []string{"127.0.0.1"}},
	}

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
	if err := service.Close(); err != nil {
		t.Fatal(err)
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
	t.Cleanup(func() { _ = service.Close() })
	if err := service.Refresh(context.Background()); err == nil {
		t.Fatal("plain HTTP EPG source unexpectedly used an HTTP proxy")
	}
	if originRequests.Load() != 1 || proxyRequests.Load() != 0 {
		t.Fatalf("blocked EPG egress used origin=%d proxy=%d", originRequests.Load(), proxyRequests.Load())
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

func TestFFmpegMemoryAdvisoryRequiresAConstrainedSelectableEngine(t *testing.T) {
	compact := resources.Plan{Profile: resources.ProfileCompact, Constrained: true}
	large := resources.Plan{Profile: resources.ProfileLarge}
	auto := config.File{Packager: config.Packager{Engine: config.EngineAuto}}
	native := config.File{Packager: config.Packager{Engine: config.EngineNative}}

	if !shouldWarnFFmpegMemory(compact, true, auto, nil) {
		t.Fatal("compact auto engine did not request the FFmpeg memory advisory")
	}
	if shouldWarnFFmpegMemory(compact, false, auto, nil) {
		t.Fatal("unavailable FFmpeg requested a memory advisory")
	}
	if shouldWarnFFmpegMemory(compact, true, native, nil) {
		t.Fatal("native-only configuration requested an FFmpeg memory advisory")
	}
	if shouldWarnFFmpegMemory(large, true, auto, nil) {
		t.Fatal("large profile requested a constrained-memory advisory")
	}
	if !shouldWarnFFmpegMemory(compact, true, native, []config.Channel{{
		ID: "compat", Packager: config.EngineFFmpeg,
	}}) {
		t.Fatal("channel FFmpeg override did not request the memory advisory")
	}
}

func TestRunReturnsFailureWhenListenAddressIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("KILN_PLAY_OPEN", "1")

	directory := t.TempDir()
	configPath := filepath.Join(directory, "kiln.toml")
	configText := fmt.Sprintf(`
[server]
listen = %q
data_dir = %q

[auth]
[[auth.users]]
username = "admin"
password_hash = "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie"
role = "admin"

[packager]
engine = "native"
`, listener.Addr().String(), directory)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		done <- run([]string{"-config", configPath})
	}()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("run succeeded with an unavailable listen address")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run waited for a signal after listen failed")
	}
}
