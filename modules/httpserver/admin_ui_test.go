//go:build !lite

package httpserver

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func requestAsset(t *testing.T, target, encoding, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if encoding != "" {
		request.Header.Set("Accept-Encoding", encoding)
	}
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	recorder := httptest.NewRecorder()
	server := &Server{}
	server.handleAdminUI(recorder, request)
	return recorder
}

func TestAdminAssetsCompressWhenAccepted(t *testing.T) {
	recorder := requestAsset(t, "/admin/assets/core/search.js", "gzip, deflate, br", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := recorder.Header().Get("Vary"); got != "Accept-Encoding" {
		t.Fatalf("Vary = %q, want Accept-Encoding", got)
	}
	if got := recorder.Header().Get("Content-Type"); got == "" {
		t.Fatal("Content-Type must stay explicit so the gzip body is not sniffed")
	}

	reader, err := gzip.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatalf("body is not valid gzip: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	original, err := adminFiles.ReadFile("admin/assets/core/search.js")
	if err != nil {
		t.Fatalf("read embedded asset: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Fatal("decompressed body does not match the embedded asset")
	}
	if recorder.Body.Len() >= len(original) {
		t.Fatalf("compressed %d bytes is not smaller than raw %d", recorder.Body.Len(), len(original))
	}
}

func TestAdminAssetsStayPlainWithoutGzip(t *testing.T) {
	for _, encoding := range []string{"", "identity", "gzip;q=0", "br"} {
		recorder := requestAsset(t, "/admin/assets/core/search.js", encoding, "")
		if got := recorder.Header().Get("Content-Encoding"); got == "gzip" {
			t.Fatalf("Accept-Encoding %q must not receive gzip", encoding)
		}
		original, err := adminFiles.ReadFile("admin/assets/core/search.js")
		if err != nil {
			t.Fatalf("read embedded asset: %v", err)
		}
		if !bytes.Equal(recorder.Body.Bytes(), original) {
			t.Fatalf("Accept-Encoding %q returned an altered body", encoding)
		}
	}
}

func TestAdminAssetsRevalidateWithETag(t *testing.T) {
	first := requestAsset(t, "/admin/assets/data/romanize.js", "gzip", "")
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag must be set so browsers can revalidate")
	}
	second := requestAsset(t, "/admin/assets/data/romanize.js", "gzip", etag)
	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Fatalf("304 must not carry a body, got %d bytes", second.Body.Len())
	}
}

func TestAdminAssetsRejectTraversal(t *testing.T) {
	for _, target := range []string{"/admin/assets/../index.html", "/admin/assets/"} {
		recorder := requestAsset(t, target, "gzip", "")
		if recorder.Code == http.StatusOK {
			t.Fatalf("%s must not resolve to an asset", target)
		}
	}
}
