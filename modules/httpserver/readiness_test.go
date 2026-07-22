//go:build !lite

package httpserver_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
)

func TestReadyAllowsNativeDASHWithoutFFmpeg(t *testing.T) {
	handler := readyHandler(t, config.EngineNative)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d: %s",
			response.Code, http.StatusOK, response.Body.String())
	}
}

func TestReadyAllowsAutoDASHWithoutFFmpeg(t *testing.T) {
	handler := readyHandler(t, config.EngineAuto)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d: %s",
			response.Code, http.StatusOK, response.Body.String())
	}
}

func TestReadyRejectsForcedFFmpegWhenUnavailable(t *testing.T) {
	handler := readyHandler(t, config.EngineFFmpeg)
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want %d: %s",
			response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
	if body := response.Body.String(); !strings.Contains(body, "ffmpeg compatibility engine is not available") {
		t.Fatalf("ready body = %s, want missing compatibility engine explanation", body)
	}
}

func readyHandler(t *testing.T, engine string) http.Handler {
	t.Helper()

	dir := t.TempDir()
	cfg := config.File{
		Server: config.Server{
			Listen:      "127.0.0.1:0",
			DataDir:     dir,
			ReadTimeout: 5,
			IdleTimeout: 30,
		},
		Channels: []config.Channel{{
			ID:        "dash",
			Title:     "DASH",
			SourceURL: "https://example.com/stream.mpd",
			Ingress:   "dash",
			Packager:  engine,
		}},
		FFmpeg: config.FFmpeg{
			Mode:   config.FFmpegModeNative,
			Binary: filepath.Join(dir, "missing-ffmpeg"),
		},
		Packager: config.Packager{Engine: config.EngineAuto},
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(cfg, db)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	obs := observe.New()
	sessions := session.NewManager(cat, nil, obs, dir, cfg.FFmpeg, httpTestKeys(), log, nil)
	server := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Log:      log,
	})
	return server.Handler()
}
