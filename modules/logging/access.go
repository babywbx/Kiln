package logging

import (
	"log/slog"
	"strings"
)

func AccessLevel(path string, status int) slog.Level {
	if status >= 400 {
		if status >= 500 {
			return slog.LevelError
		}
		return slog.LevelWarn
	}
	if isQuietPath(path) {
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

func isQuietPath(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/":
		return true
	}
	if strings.Contains(path, "/live/") {
		return true
	}
	if strings.Contains(path, "/u/") {
		return true
	}
	return false
}
