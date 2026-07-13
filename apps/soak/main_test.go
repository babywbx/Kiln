package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunExitStatusReflectsSoakResult(t *testing.T) {
	t.Parallel()
	t.Run("success", func(t *testing.T) {
		t.Parallel()
		var sequence atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/v1/play/news/index.m3u8":
				seq := sequence.Add(1)
				_, _ = fmt.Fprintf(w, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:%d\n#EXTINF:1,\nseg-%d.m4s\n", seq, seq)
			case strings.HasPrefix(r.URL.Path, "/v1/play/news/seg-"):
				_, _ = w.Write([]byte("media"))
			default:
				http.NotFound(w, r)
			}
		}))
		defer server.Close()
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"-server", server.URL,
			"-channels", "news",
			"-duration", "15ms",
			"-interval", "5ms",
			"-stall-timeout", "1s",
			"-status-path", "",
			"-metrics-path", "",
		}, &stdout, &stderr)
		if code != 0 || !strings.Contains(stdout.String(), `"type":"final"`) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})

	t.Run("failure", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		}))
		defer server.Close()
		var stdout, stderr bytes.Buffer
		code := run([]string{
			"-server", server.URL,
			"-channels", "news",
			"-duration", "1s",
			"-interval", "5ms",
			"-max-consecutive-errors", "1",
			"-status-path", "",
			"-metrics-path", "",
		}, &stdout, &stderr)
		if code != 1 || !strings.Contains(stdout.String(), `"failed":true`) {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	})
}
