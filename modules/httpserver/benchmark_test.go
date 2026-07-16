package httpserver_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
)

func TestBusinessHandlerDoesNotExposePprof(t *testing.T) {
	cfg := config.File{Server: config.Server{ReadTimeout: 5, IdleTimeout: 30}}
	server := httpserver.New(httpserver.Deps{
		Cfg:     cfg,
		Catalog: catalog.New(cfg, nil),
		Observe: observe.New(),
		Log:     benchmarkLogger(),
	})
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusNotFound {
		t.Fatalf("business pprof status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func BenchmarkDistributionMediaHotPath(b *testing.B) {
	server, path := newDistributionBenchmarkServer(b)

	b.Run("sequential", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, path, nil)
			server.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				b.Fatalf("status = %d", response.Code)
			}
		}
	})

	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, path, nil)
				server.Handler().ServeHTTP(response, request)
				if response.Code != http.StatusNotFound {
					b.Errorf("status = %d", response.Code)
					return
				}
			}
		})
	})
}

func newDistributionBenchmarkServer(b *testing.B) (*httpserver.Server, string) {
	b.Helper()
	directory := b.TempDir()
	cfg := config.File{
		Server: config.Server{
			PublicBaseURL: "http://kiln.test",
			DataDir:       directory,
			ReadTimeout:   5,
			IdleTimeout:   30,
		},
		Channels: []config.Channel{{
			ID:        "news",
			Title:     "News",
			SourceURL: "https://example.test/live.m3u8",
			Ingress:   "hls",
			OnDemand:  true,
		}},
	}
	db, err := store.Open(directory)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = db.Close() })
	if err := db.SeedFromConfig(cfg); err != nil {
		b.Fatal(err)
	}
	rawToken, row, err := accesstoken.NewRow("Benchmark", "", nil)
	if err != nil {
		b.Fatal(err)
	}
	if err := db.InsertAccessToken(row); err != nil {
		b.Fatal(err)
	}
	obs := observe.New()
	cat := catalog.New(cfg, db)
	sessions := session.NewManager(
		cat,
		nil,
		obs,
		directory,
		config.FFmpeg{},
		benchmarkLogger(),
		nil,
	)
	active, err := sessions.Acquire("news")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { sessions.StopChannel("news") })
	_, generation := active.PublicationSnapshot()
	server := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Store:    db,
		Log:      benchmarkLogger(),
	})
	return server, "/p/" + rawToken + "/play/news/live/missing.ts?g=" + generation
}

func benchmarkLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
