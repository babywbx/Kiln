//go:build !lite

package httpserver

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed admin/index.html admin/assets/*
var adminFiles embed.FS

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
