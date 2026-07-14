package soak

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerTracksPlaylistProgressAndEndpoints(t *testing.T) {
	t.Parallel()
	var sequence atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/play/news/index.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nvideo.m3u8\n")
		case r.URL.Path == "/v1/play/news/video.m3u8":
			seq := sequence.Add(1)
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXTINF:1,\nseg-%d.m4s\n", seq, seq)
		case strings.HasPrefix(r.URL.Path, "/v1/play/news/seg-"):
			_, _ = w.Write([]byte("media"))
		case r.URL.Path == "/v1/status":
			_, _ = w.Write([]byte(`{"uptime_sec":1,"goroutines":9,"session_count":1}`))
		case r.URL.Path == "/metrics":
			_, _ = w.Write([]byte("kiln_goroutines 9\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var output strings.Builder
	runner, err := New(Config{
		BaseURL:        server.URL,
		Channels:       []string{"news"},
		Duration:       45 * time.Millisecond,
		Interval:       10 * time.Millisecond,
		StallTimeout:   100 * time.Millisecond,
		RequestTimeout: time.Second,
		StatusPath:     "/v1/status",
		MetricsPath:    "/metrics",
	}, WithOutput(&output))
	if err != nil {
		t.Fatal(err)
	}

	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Failed || report.Cancelled {
		t.Fatalf("unexpected report state: %+v", report)
	}
	if report.Channels[0].ProgressEvents < 2 || report.Channels[0].SegmentRequests == 0 {
		t.Fatalf("progress not observed: %+v", report.Channels[0])
	}
	if report.Channels[0].HTTPErrors != 0 {
		t.Fatalf("unexpected HTTP errors: %+v", report.Channels[0])
	}
	if report.StatusRequests == 0 || report.MetricsRequests == 0 {
		t.Fatalf("optional endpoints not checked: %+v", report)
	}
	assertFinalJSONL(t, output.String(), false, false)
}

func TestRunnerFailsWhenMediaSequenceStalls(t *testing.T) {
	t.Parallel()
	server := staticHLSServer()
	defer server.Close()

	var output strings.Builder
	runner, err := New(Config{
		BaseURL:        server.URL,
		Channels:       []string{"news"},
		Duration:       200 * time.Millisecond,
		Interval:       10 * time.Millisecond,
		StallTimeout:   35 * time.Millisecond,
		RequestTimeout: time.Second,
	}, WithOutput(&output))
	if err != nil {
		t.Fatal(err)
	}

	report, err := runner.Run(context.Background())
	if !errors.Is(err, ErrStalled) {
		t.Fatalf("want ErrStalled, got %v", err)
	}
	if !report.Failed || report.Channels[0].Stalls != 1 {
		t.Fatalf("stall missing from report: %+v", report)
	}
	assertFinalJSONL(t, output.String(), true, false)
}

func TestRunnerFailsWhenMediaSequenceRegresses(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/play/news/index.m3u8":
			sequence := int64(10)
			if requests.Add(1) > 1 {
				sequence = 1
			}
			_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXTINF:1,\nseg-%d.m4s\n", sequence, sequence)
		case "/v1/play/news/seg-10.m4s", "/v1/play/news/seg-1.m4s":
			_, _ = w.Write([]byte("media"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner, err := New(Config{
		BaseURL:        server.URL,
		Channels:       []string{"news"},
		Duration:       time.Second,
		Interval:       5 * time.Millisecond,
		StallTimeout:   time.Second,
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background())
	if !errors.Is(err, ErrSequenceRegressed) {
		t.Fatalf("want ErrSequenceRegressed, got %v", err)
	}
	if report.Channels[0].SequenceRegressions != 1 {
		t.Fatalf("sequence regression missing from report: %+v", report.Channels[0])
	}
}

func TestParsePrometheusMetricsKeepsPackagerSeries(t *testing.T) {
	t.Parallel()
	metrics := parsePrometheusMetrics([]byte("kiln_goroutines 9\nkiln_packager_reanchors_total{channel=\"news\"} 3\n"))
	if metrics["kiln_goroutines"] != 9 || metrics[`kiln_packager_reanchors_total{channel="news"}`] != 3 {
		t.Fatalf("packager metrics missing: %#v", metrics)
	}
}

func TestRunnerCancellationStillWritesFinalReport(t *testing.T) {
	t.Parallel()
	server := staticHLSServer()
	defer server.Close()

	var output strings.Builder
	runner, err := New(Config{
		BaseURL:        server.URL,
		Channels:       []string{"news"},
		Duration:       time.Hour,
		Interval:       time.Second,
		StallTimeout:   time.Minute,
		RequestTimeout: time.Second,
	}, WithOutput(&output))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	report, err := runner.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
	if !report.Cancelled {
		t.Fatalf("report not marked cancelled: %+v", report)
	}
	assertFinalJSONL(t, output.String(), false, true)
}

func TestRunnerDiscoversChannelsWithBearerToken(t *testing.T) {
	t.Parallel()
	var authorized atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		authorized.Store(true)
		switch r.URL.Path {
		case "/v1/channels":
			_, _ = w.Write([]byte(`{"channels":[{"id":"news"}]}`))
		case "/v1/play/news/index.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\nseg.m4s\n")
		case "/v1/play/news/seg.m4s":
			_, _ = w.Write([]byte("media"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner, err := New(Config{
		BaseURL:        server.URL,
		Token:          "secret",
		Duration:       20 * time.Millisecond,
		Interval:       5 * time.Millisecond,
		StallTimeout:   time.Second,
		RequestTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !authorized.Load() || len(report.Channels) != 1 || report.Channels[0].ID != "news" {
		t.Fatalf("channel discovery failed: %+v", report)
	}
}

func TestRunnerFailsAfterConsecutiveHTTPErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	runner, err := New(Config{
		BaseURL:              server.URL,
		Channels:             []string{"news"},
		Duration:             time.Second,
		Interval:             5 * time.Millisecond,
		StallTimeout:         time.Second,
		RequestTimeout:       time.Second,
		MaxConsecutiveErrors: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background())
	if !errors.Is(err, ErrTooManyErrors) {
		t.Fatalf("want error threshold, got %v", err)
	}
	if report.Channels[0].HTTPErrors != 2 || report.Channels[0].ConsecutiveErrors != 2 {
		t.Fatalf("HTTP failures not tracked: %+v", report.Channels[0])
	}
}

func TestRunnerCanLoginWithoutWritingTokenToReport(t *testing.T) {
	t.Parallel()
	const token = "highly-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/login" {
			_, _ = fmt.Fprintf(w, `{"token":%q}`, token)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/channels":
			_, _ = w.Write([]byte(`{"channels":[{"id":"news"}]}`))
		case "/v1/play/news/index.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\nseg.m4s\n")
		case "/v1/play/news/seg.m4s":
			_, _ = w.Write([]byte("media"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var output strings.Builder
	runner, err := New(Config{
		BaseURL:        server.URL,
		Username:       "admin",
		Password:       "password",
		Duration:       15 * time.Millisecond,
		Interval:       5 * time.Millisecond,
		StallTimeout:   time.Second,
		RequestTimeout: time.Second,
	}, WithOutput(&output))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), token) || strings.Contains(output.String(), "password") {
		t.Fatalf("credentials leaked into JSONL: %s", output.String())
	}
}

func TestDefaultsToTwentyFourHours(t *testing.T) {
	t.Parallel()
	runner, err := New(Config{BaseURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if runner.cfg.Duration != 24*time.Hour {
		t.Fatalf("duration = %s", runner.cfg.Duration)
	}
}

func TestParseMediaPlaylistUsesNewestLowLatencyPart(t *testing.T) {
	t.Parallel()
	playlist, err := parseMediaPlaylist([]byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:12\n#EXTINF:1,\nseg-12.m4s\n#EXT-X-PART:DURATION=0.2,URI=\"part-13-0.m4s\"\n#EXT-X-PART:DURATION=0.2,URI=\"part-13-1.m4s\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if playlist.EndSequence != 12 || playlist.LatestAsset != "part-13-1.m4s" {
		t.Fatalf("unexpected LL-HLS edge: %+v", playlist)
	}
}

func staticHLSServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/play/news/index.m3u8":
			_, _ = fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\nseg.m4s\n")
		case "/v1/play/news/seg.m4s":
			_, _ = w.Write([]byte("media"))
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertFinalJSONL(t *testing.T, raw string, failed, cancelled bool) {
	t.Helper()
	var lines []map[string]any
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("invalid JSONL %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) < 2 {
		t.Fatalf("want snapshots and final report, got %q", raw)
	}
	last := lines[len(lines)-1]
	if last["type"] != "final" || last["failed"] != failed || last["cancelled"] != cancelled {
		t.Fatalf("unexpected final JSONL object: %#v", last)
	}
}
