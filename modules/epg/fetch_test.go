package epg_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if !bytes.Equal(result.Data, want) {
		t.Fatalf("data = %q, want %q", result.Data, want)
	}
	if result.Metadata.ETag != `"new"` || result.Metadata.LastModified != "Mon, 13 Jul 2026 00:00:00 GMT" {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
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
	if !bytes.Equal(result.Data, want) {
		t.Fatalf("data = %q, want %q", result.Data, want)
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

	_, err := (&epg.Fetcher{Client: server.Client(), MaxSourceBytes: 1024}).Fetch(
		context.Background(), epg.Source{ID: "large", URL: server.URL}, epg.CacheMetadata{},
	)
	if !errors.Is(err, epg.ErrSourceTooLarge) {
		t.Fatalf("error = %v, want ErrSourceTooLarge", err)
	}
}

func TestFetcherResolvesAClientPerSource(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<tv></tv>`))
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
	if _, err := fetcher.Fetch(context.Background(), source, epg.CacheMetadata{}); err != nil {
		t.Fatal(err)
	}
	if resolved.ID != source.ID || resolved.Proxy != "lan-http" {
		t.Fatalf("resolved source = %+v, want %+v", resolved, source)
	}
}
