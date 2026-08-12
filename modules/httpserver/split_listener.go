//go:build !lite

package httpserver

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/babywbx/kiln/modules/apperr"
)

func (s *Server) plaintextSurfaceAllows(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/metrics", "/v1/epg.xml", "/v1/epg.xml.gz":
		return true
	}
	if strings.HasPrefix(path, "/p/") || strings.HasPrefix(path, "/v1/logo/") {
		return true
	}
	if s.deps.Cfg.Security.PlayAuthRequired() {
		return false
	}
	return path == "/v1/playlist.m3u" || strings.HasPrefix(path, "/v1/play/")
}

func (s *Server) plaintextSurface(next http.Handler, tlsAddr string) http.Handler {
	_, tlsPort, err := net.SplitHostPort(tlsAddr)
	if err != nil {
		tlsPort = ""
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead ||
			r.Header.Get("Authorization") != "" || r.URL.Query().Has("token") {
			writeTLSRequired(w)
			return
		}
		if s.plaintextSurfaceAllows(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !s.requestHostAllowed(r) {
			writeAppErr(w, apperr.New(apperr.CodeForbidden, http.StatusForbidden, "host not allowed"))
			return
		}
		target := secureRedirectTarget(r, tlsPort)
		if target == "" {
			writeTLSRequired(w)
			return
		}
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})
}

func writeTLSRequired(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"error": map[string]any{"code": "tls_required", "message": "this endpoint is only served over https"},
	})
}

func secureRedirectTarget(r *http.Request, tlsPort string) string {
	parsed, err := url.Parse("//" + r.Host)
	if err != nil || parsed.User != nil {
		return ""
	}
	host := parsed.Hostname()
	if host == "" {
		return ""
	}
	if tlsPort != "" && tlsPort != "443" {
		host = net.JoinHostPort(host, tlsPort)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host + r.URL.RequestURI()
}
