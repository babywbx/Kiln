package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConsoleFormat(t *testing.T) {
	var buf bytes.Buffer
	h := newConsoleHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}, false)

	ts := time.Date(2026, 7, 11, 15, 4, 5, 0, time.Local)

	rec := slog.NewRecord(ts, slog.LevelInfo, "kiln starting", 0)
	rec.Add("version", "0.1.0", "listen", "0.0.0.0:8080", "channels", 2)
	if err := h.Handle(context.TODO(), rec); err != nil {
		t.Fatal(err)
	}

	rec = slog.NewRecord(ts, slog.LevelInfo, "listening", 0)
	rec.Add("addr", "0.0.0.0:8080", "version", "0.1.0")
	_ = h.Handle(context.TODO(), rec)

	rec = slog.NewRecord(ts, slog.LevelInfo, "session started", 0)
	rec.Add("channel", "channel-1", "ingress", "hls", "pack_mode", "copy")
	_ = h.Handle(context.TODO(), rec)

	rec = slog.NewRecord(ts, slog.LevelError, "session restart failed", 0)
	rec.Add("channel", "movie-uhd", "err", "connection refused")
	_ = h.Handle(context.TODO(), rec)

	rec = slog.NewRecord(ts, slog.LevelInfo, "request", 0)
	rec.Add("remote", "203.0.113.10", "method", "GET", "path", "/v1/me", "status", 200, "dur_ms", 18, "request_id", "abc")
	_ = h.Handle(context.TODO(), rec)

	rec = slog.NewRecord(ts, slog.LevelWarn, "session restarting", 0)
	rec.Add("channel", "channel-1", "attempt", 1, "delay", "3s", "err", "ffmpeg exited")
	_ = h.Handle(context.TODO(), rec)

	rec = slog.NewRecord(ts, slog.LevelInfo, "request", 0)
	rec.Add("remote", "10.0.0.1", "method", "GET", "path", "/v1/play/x/live/seg.ts", "status", 200, "dur_ms", 2)
	_ = h.Handle(context.TODO(), rec)

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := []string{
		"2026-07-11 15:04:05  INFO   kiln starting  listen=0.0.0.0:8080 version=0.1.0 channels=2",
		"2026-07-11 15:04:05  INFO   listening  addr=0.0.0.0:8080 version=0.1.0",
		"2026-07-11 15:04:05  INFO   [channel-1] session started  ingress=hls pack_mode=copy",
		`2026-07-11 15:04:05  ERROR  [movie-uhd] session restart failed  err="connection refused"`,
		"2026-07-11 15:04:05  INFO   request  remote=203.0.113.10 method=GET path=/v1/me status=200 dur_ms=18 request_id=abc",
		`2026-07-11 15:04:05  WARN   [channel-1] session restarting  attempt=1 delay=3s err="ffmpeg exited"`,
		"2026-07-11 15:04:05  INFO   request  remote=10.0.0.1 method=GET path=/v1/play/x/live/seg.ts status=200 dur_ms=2",
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines:\n%s", len(lines), out)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d\n got: %s\nwant: %s", i, lines[i], want[i])
		}
	}
}

func TestQuoteSpaces(t *testing.T) {
	var buf bytes.Buffer
	h := newConsoleHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	rec := slog.NewRecord(ts, slog.LevelError, "ffmpeg exited", 0)
	rec.Add("err", "signal: killed", "stderr", "line one")
	_ = h.Handle(context.TODO(), rec)
	got := strings.TrimSpace(buf.String())
	want := `2026-01-02 03:04:05  ERROR  ffmpeg exited  err="signal: killed" stderr="line one"`
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

func TestAccessLevel(t *testing.T) {
	cases := []struct {
		path   string
		status int
		want   slog.Level
	}{
		{"/healthz", 200, slog.LevelDebug},
		{"/readyz", 200, slog.LevelDebug},
		{"/v1/play/c1/live/seg0.ts", 200, slog.LevelDebug},
		{"/v1/play/c1/u/seg", 200, slog.LevelDebug},
		{"/v1/play/c1/index.m3u8", 200, slog.LevelInfo},
		{"/v1/me", 200, slog.LevelInfo},
		{"/v1/me", 401, slog.LevelWarn},
		{"/v1/play/c1/live/x.ts", 500, slog.LevelError},
		{"/admin", 200, slog.LevelInfo},
	}
	for _, tc := range cases {
		if got := AccessLevel(tc.path, tc.status); got != tc.want {
			t.Errorf("AccessLevel(%q,%d)=%v want %v", tc.path, tc.status, got, tc.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if NormalizeFormat("JSON") != "json" {
		t.Fatal()
	}
	if NormalizeFormat("console") != "text" {
		t.Fatal()
	}
	if ParseLevel("WRN") != slog.LevelWarn {
		t.Fatal()
	}
	if NormalizeColor("ALWAYS") != "always" {
		t.Fatal()
	}
}

func TestJSONHasService(t *testing.T) {
	var buf bytes.Buffer
	log := NewWith(Options{Level: "info", Format: "json", Color: "never", Output: &buf})
	log.Info("listening", "addr", ":8080")
	if !strings.Contains(buf.String(), `"service":"kiln"`) {
		t.Fatalf("json missing service: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"msg":"listening"`) {
		t.Fatalf("json missing msg: %s", buf.String())
	}
}

func TestWithAttrsChannel(t *testing.T) {
	var buf bytes.Buffer
	base := newConsoleHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	log := slog.New(base.WithAttrs([]slog.Attr{slog.String("channel", "news")}))
	// fix time via direct handle
	ts := time.Date(2026, 7, 11, 0, 0, 0, 0, time.Local)
	rec := slog.NewRecord(ts, slog.LevelInfo, "dash packager ready", 0)
	rec.Add("mode", "cenc")
	// need handler that already has channel — use WithAttrs handler
	h := base.WithAttrs([]slog.Attr{slog.String("channel", "news")})
	_ = h.Handle(context.TODO(), rec)
	got := strings.TrimSpace(buf.String())
	want := "2026-07-11 00:00:00  INFO   [news] dash packager ready  mode=cenc"
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
	_ = log
}

func TestDerivedHandlersShareTheLock(t *testing.T) {
	var buf bytes.Buffer
	base := newConsoleHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}, false)
	derived := base.WithAttrs([]slog.Attr{slog.String("channel", "news")})
	grouped := base.WithGroup("egress")

	ts := time.Date(2026, 7, 11, 0, 0, 0, 0, time.Local)
	var wg sync.WaitGroup
	for _, h := range []slog.Handler{base, derived, grouped} {
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(h slog.Handler) {
				defer wg.Done()
				_ = h.Handle(context.Background(), slog.NewRecord(ts, slog.LevelInfo, "concurrent", 0))
			}(h)
		}
	}
	wg.Wait()

	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if !strings.HasSuffix(line, "concurrent") {
			t.Fatalf("interleaved log line, derived handlers are not sharing the lock: %q", line)
		}
	}
}
