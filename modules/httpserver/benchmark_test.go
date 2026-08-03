//go:build !lite

package httpserver_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/auth"
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
	fixture := newDistributionBenchmarkServer(b)

	for _, tc := range []struct {
		name      string
		path      string
		bearer    string
		requestID bool
	}{
		{name: "path_token_master", path: fixture.pathTokenMaster, requestID: true},
		{name: "path_token_media", path: fixture.pathTokenMedia, requestID: true},
		{name: "path_token_segment", path: fixture.pathTokenSegment, requestID: true},
		{name: "path_token_segment_without_request_id", path: fixture.pathTokenSegment},
		{name: "scoped_path_token_segment", path: fixture.scopedPathTokenSegment, requestID: true},
		{name: "jwt_segment", path: fixture.jwtSegment, bearer: fixture.jwt, requestID: true},
	} {
		b.Run(tc.name, func(b *testing.B) {
			benchmarkHandler(b, fixture.server.Handler(), tc.path, tc.bearer, tc.requestID)
		})
	}
}

func BenchmarkDistributionMediaLoopback(b *testing.B) {
	fixture := newDistributionBenchmarkServer(b)
	testServer := httptest.NewServer(fixture.server.Handler())
	b.Cleanup(testServer.Close)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 64
	transport.MaxIdleConnsPerHost = 64
	b.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	url := testServer.URL + fixture.pathTokenSegment

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			request, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				b.Error(err)
				return
			}
			response, err := client.Do(request)
			if err != nil {
				b.Error(err)
				return
			}
			_, copyErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if copyErr != nil || closeErr != nil || response.StatusCode != http.StatusOK {
				b.Errorf("status = %d, copy = %v, close = %v", response.StatusCode, copyErr, closeErr)
				return
			}
		}
	})
}

func benchmarkHandler(b *testing.B, handler http.Handler, path, bearer string, presetRequestID bool) {
	b.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	if presetRequestID {
		request.Header.Set("X-Request-ID", "benchmark")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response := newBenchmarkResponseWriter()

	handler.ServeHTTP(response, request)
	if response.status != http.StatusOK || response.written == 0 {
		b.Fatalf("warmup status = %d, bytes = %d", response.status, response.written)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if !presetRequestID {
			request.Header.Del("X-Request-ID")
		}
		response.reset()
		handler.ServeHTTP(response, request)
		if response.status != http.StatusOK || response.written == 0 {
			b.Fatalf("status = %d, bytes = %d", response.status, response.written)
		}
	}
}

type benchmarkResponseWriter struct {
	header  http.Header
	status  int
	written int64
}

func newBenchmarkResponseWriter() *benchmarkResponseWriter {
	w := &benchmarkResponseWriter{header: make(http.Header, 16)}
	w.reset()
	return w
}

func (w *benchmarkResponseWriter) Header() http.Header { return w.header }

func (w *benchmarkResponseWriter) WriteHeader(status int) { w.status = status }

func (w *benchmarkResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.written += int64(len(body))
	return len(body), nil
}

func (w *benchmarkResponseWriter) reset() {
	clear(w.header)
	w.status = http.StatusOK
	w.written = 0
}

type distributionBenchmarkFixture struct {
	server                 *httpserver.Server
	pathTokenMaster        string
	pathTokenMedia         string
	pathTokenSegment       string
	scopedPathTokenSegment string
	jwtSegment             string
	jwt                    string
}

func newDistributionBenchmarkServer(b *testing.B) distributionBenchmarkFixture {
	b.Helper()
	directory := b.TempDir()
	cfg := config.File{
		Server: config.Server{
			PublicBaseURL: "http://kiln.test",
			DataDir:       directory,
			ReadTimeout:   5,
			IdleTimeout:   30,
		},
		Security: config.Security{
			PlayRequireAuth:  config.Bool(true),
			MaxPlaylistBytes: 1 << 20,
			MaxBodyBytes:     1 << 20,
		},
		Upstreams: []config.Upstream{{
			ID:      "origin",
			BaseURL: "http://origin.invalid",
		}},
		Channels: []config.Channel{{
			ID:             "news",
			Title:          "News",
			Upstream:       "origin",
			Path:           "/stream.mpd",
			Ingress:        "dash",
			OnDemand:       true,
			IdleTimeoutSec: 30,
		}},
		Packager: config.Packager{Engine: config.EngineAuto, PlaylistSize: 8, GraceSec: 30},
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
	scopedRawToken, scopedRow, err := accesstoken.NewRow("Scoped benchmark", "", []string{"news"})
	if err != nil {
		b.Fatal(err)
	}
	if err := db.InsertAccessToken(scopedRow); err != nil {
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
		httpTestKeys(),
		benchmarkLogger(),
		nil,
	)
	sessions.SetPackager(fakePackager{})
	active, err := sessions.Acquire("news")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { sessions.StopChannel("news") })
	_, generation := active.PublicationSnapshot()
	authService, err := auth.NewForTest(nil, time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	jwt, _, err := authService.IssuePreview("news", time.Hour)
	if err != nil {
		b.Fatal(err)
	}
	server := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Auth:     authService,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Store:    db,
		Log:      benchmarkLogger(),
	})
	distPrefix := "/p/" + rawToken + "/play/news"
	scopedDistPrefix := "/p/" + scopedRawToken + "/play/news"
	generationQuery := "?g=" + generation
	return distributionBenchmarkFixture{
		server:                 server,
		pathTokenMaster:        distPrefix + "/index.m3u8",
		pathTokenMedia:         distPrefix + "/live/video-main.m3u8" + generationQuery,
		pathTokenSegment:       distPrefix + "/live/video-main-000001.m4s" + generationQuery,
		scopedPathTokenSegment: scopedDistPrefix + "/live/video-main-000001.m4s" + generationQuery,
		jwtSegment:             "/v1/play/news/live/video-main-000001.m4s" + generationQuery,
		jwt:                    jwt,
	}
}

func benchmarkLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
