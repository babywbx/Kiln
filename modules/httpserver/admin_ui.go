//go:build !lite

package httpserver

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
)

//go:embed admin/index.html admin/assets/*
var adminFiles embed.FS

var compressedAssets sync.Map

type compressedAsset struct {
	body []byte
	etag string
	kind string
}

func (s *Server) handleAdminUI(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/admin/assets/") {
		assets, err := fs.Sub(adminFiles, "admin/assets")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/admin/assets/")
		if name == "" || name == "." || strings.Contains(name, "..") {
			http.NotFound(w, r)
			return
		}
		if asset, ok := compressibleAsset(assets, name); ok && acceptsGzip(r) {
			serveCompressed(w, r, asset)
			return
		}
		http.ServeFileFS(w, r, assets, name)
		return
	}

	body, err := adminFiles.ReadFile("admin/index.html")
	if err != nil {
		http.Error(w, "admin unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func compressibleType(name string) string {
	switch path.Ext(name) {
	case ".js", ".mjs", ".css", ".json", ".svg", ".map", ".html", ".txt":
		return mime.TypeByExtension(path.Ext(name))
	default:
		return ""
	}
}

func compressibleAsset(assets fs.FS, name string) (compressedAsset, bool) {
	kind := compressibleType(name)
	if kind == "" {
		return compressedAsset{}, false
	}
	if cached, ok := compressedAssets.Load(name); ok {
		asset, ok := cached.(compressedAsset)
		return asset, ok
	}
	raw, err := fs.ReadFile(assets, name)
	if err != nil {
		return compressedAsset{}, false
	}
	var buffer bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buffer, gzip.BestCompression)
	if err != nil {
		return compressedAsset{}, false
	}
	if _, err := writer.Write(raw); err != nil {
		return compressedAsset{}, false
	}
	if err := writer.Close(); err != nil {
		return compressedAsset{}, false
	}
	if buffer.Len() >= len(raw) {
		return compressedAsset{}, false
	}
	sum := sha256.Sum256(raw)
	asset := compressedAsset{
		body: buffer.Bytes(),
		etag: `"` + hex.EncodeToString(sum[:16]) + `"`,
		kind: kind,
	}
	compressedAssets.Store(name, asset)
	return asset, true
}

func serveCompressed(w http.ResponseWriter, r *http.Request, asset compressedAsset) {
	header := w.Header()
	header.Set("Content-Type", asset.kind)
	header.Set("Content-Encoding", "gzip")
	header.Set("Vary", "Accept-Encoding")
	header.Set("ETag", asset.etag)
	header.Set("Cache-Control", "no-cache")
	if matchesETag(r.Header.Get("If-None-Match"), asset.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	header.Set("Content-Length", strconv.Itoa(len(asset.body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(asset.body)
	}
}

func matchesETag(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(fields[0]), "gzip") {
			continue
		}
		for _, parameter := range fields[1:] {
			parameter = strings.ReplaceAll(strings.TrimSpace(parameter), " ", "")
			if strings.EqualFold(parameter, "q=0") || strings.EqualFold(parameter, "q=0.0") ||
				strings.EqualFold(parameter, "q=0.00") || strings.EqualFold(parameter, "q=0.000") {
				return false
			}
		}
		return true
	}
	return false
}
