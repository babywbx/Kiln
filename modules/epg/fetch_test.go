package epg_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/epg"
)

func TestFetcherSendsValidatorsAndDecodesGzip(t *testing.T) {
	t.Parallel()

	want := []byte(`<tv><channel id="one"></channel></tv>`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("If-None-Match"); got != `"old"` {
			t.Errorf("If-None-Match = %q", got)
		}
		if got := r.Header.Get("If-Modified-Since"); got != "Sun, 12 Jul 2026 00:00:00 GMT" {
			t.Errorf("If-Modified-Since = %q", got)
		}
		if got := r.Header.Get("Accept-Encoding"); got != "gzip" {
			t.Errorf("Accept-Encoding = %q", got)
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("ETag", `"new"`)
		w.Header().Set("Last-Modified", "Mon, 13 Jul 2026 00:00:00 GMT")
		writer := gzip.NewWriter(w)
		_, _ = writer.Write(want)
		_ = writer.Close()
	}))
	defer server.Close()

	fetcher := &epg.Fetcher{Client: server.Client(), MaxSourceBytes: 1024}
	result, err := fetcher.Fetch(context.Background(), epg.Source{ID: "test", URL: server.URL}, epg.CacheMetadata{
		ETag: `"old"`, LastModified: "Sun, 12 Jul 2026 00:00:00 GMT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFetched(t, result); !bytes.Equal(got, want) {
		t.Fatalf("data = %q, want %q", got, want)
	}
	if result.Metadata.ETag != `"new"` || result.Metadata.LastModified != "Mon, 13 Jul 2026 00:00:00 GMT" {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
}

func readFetched(t *testing.T, result epg.FetchResult) []byte {
	t.Helper()
	defer func() { _ = result.Close() }()
	data, err := io.ReadAll(result.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFetcherHandlesNotModified(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	previous := epg.CacheMetadata{ETag: `"same"`, LastModified: "Sun, 12 Jul 2026 00:00:00 GMT"}
	result, err := (&epg.Fetcher{Client: server.Client()}).Fetch(
		context.Background(), epg.Source{ID: "test", URL: server.URL}, previous,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NotModified || result.Metadata.ETag != previous.ETag {
		t.Fatalf("result = %+v", result)
	}
}

func TestFetcherDetectsGzipFileWithoutContentEncoding(t *testing.T) {
	t.Parallel()

	want := []byte(`<tv></tv>`)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(want)
	_ = writer.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	result, err := (&epg.Fetcher{Client: server.Client(), MaxSourceBytes: 1024}).Fetch(
		context.Background(), epg.Source{ID: "test", URL: server.URL + "/epg.xml.gz"}, epg.CacheMetadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFetched(t, result); !bytes.Equal(got, want) {
		t.Fatalf("data = %q, want %q", got, want)
	}
}

func TestFetcherLimitsDecompressedBytes(t *testing.T) {
	t.Parallel()

	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), 1025))
	_ = writer.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed.Bytes())
	}))
	defer server.Close()

	result, err := (&epg.Fetcher{Client: server.Client(), MaxSourceBytes: 1024}).Fetch(
		context.Background(), epg.Source{ID: "large", URL: server.URL}, epg.CacheMetadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	if _, err := io.ReadAll(result.Body); !errors.Is(err, epg.ErrSourceTooLarge) {
		t.Fatalf("error = %v, want ErrSourceTooLarge", err)
	}
}

func TestFetcherResolvesAClientPerSource(t *testing.T) {
	t.Parallel()

	want := bytes.Repeat([]byte("0123456789abcdef"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(want)
	}))
	defer server.Close()

	var resolved epg.Source
	fetcher := &epg.Fetcher{
		ClientForSource: func(source epg.Source) (*http.Client, error) {
			resolved = source
			return server.Client(), nil
		},
	}
	source := epg.Source{ID: "routed", URL: server.URL, Proxy: "lan-http"}
	result, err := fetcher.Fetch(context.Background(), source, epg.CacheMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFetched(t, result); !bytes.Equal(got, want) {
		t.Fatalf("data length = %d, want %d", len(got), len(want))
	}
	if resolved.ID != source.ID || resolved.Proxy != "lan-http" {
		t.Fatalf("resolved source = %+v, want %+v", resolved, source)
	}
}

func TestFetcherStartsBodyTimeoutWhenStreamingStarts(t *testing.T) {
	t.Parallel()

	want := bytes.Repeat([]byte("0123456789abcdef"), 1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(want)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	result, err := (&epg.Fetcher{Client: client}).Fetch(
		context.Background(), epg.Source{ID: "queued", URL: server.URL}, epg.CacheMetadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * client.Timeout)
	if got := readFetched(t, result); !bytes.Equal(got, want) {
		t.Fatalf("data length = %d, want %d", len(got), len(want))
	}
}

func TestFetcherTimesOutStalledStreamingReads(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<t")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 50 * time.Millisecond
	result, err := (&epg.Fetcher{Client: client}).Fetch(
		context.Background(), epg.Source{ID: "stalled", URL: server.URL}, epg.CacheMetadata{},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	if _, err := io.ReadAll(result.Body); err == nil {
		t.Fatal("stalled body read succeeded")
	}
}
