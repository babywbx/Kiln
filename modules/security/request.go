package security

import (
	"net"
	"net/http"
	"strings"
)

func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func IsLocalHealthRequest(r *http.Request) bool {
	if r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
		return false
	}
	return net.ParseIP(ClientIP(r)).IsLoopback()
}

func RequestHostAllowed(r *http.Request, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	host := r.Host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	for _, candidate := range allowed {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "*" || strings.Trim(candidate, "[]") == host {
			return true
		}
	}
	return false
}

func ApplyCORS(w http.ResponseWriter, r *http.Request, allowed []string) {
	if len(allowed) == 0 {
		return
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	matched := false
	for _, candidate := range allowed {
		if candidate == "*" || strings.EqualFold(candidate, origin) {
			matched = true
			if candidate != "*" {
				origin = candidate
			}
			break
		}
	}
	if !matched {
		return
	}
	if len(allowed) == 1 && allowed[0] == "*" {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
	w.Header().Set("Access-Control-Max-Age", "600")
}
