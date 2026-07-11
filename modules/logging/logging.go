package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options configure the process logger.
//
//	format: text (default, human console) | json (machines / collectors)
//	color:  auto (default, TTY only) | always | never — text only
type Options struct {
	Level  string
	Format string
	Color  string
	// Output defaults to os.Stdout.
	Output io.Writer
}

// New builds the process logger from level/format strings.
func New(level, format string) *slog.Logger {
	return NewWith(Options{Level: level, Format: format, Color: "auto"})
}

// NewWith builds the process logger from Options.
func NewWith(opt Options) *slog.Logger {
	out := opt.Output
	if out == nil {
		out = os.Stdout
	}
	lv := ParseLevel(opt.Level)
	format := NormalizeFormat(opt.Format)
	opts := &slog.HandlerOptions{Level: lv}

	switch format {
	case "json":
		h := slog.NewJSONHandler(out, opts)
		return slog.New(h).With("service", "kiln")
	default:
		h := newConsoleHandler(out, opts, resolveColor(opt.Color, out))
		return slog.New(h)
	}
}

// Bootstrap returns a minimal logger for use before config is loaded.
func Bootstrap() *slog.Logger {
	return NewWith(Options{Level: "info", Format: "text", Color: "auto"})
}

// ParseLevel maps config/env strings to slog levels.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "dbg", "trace":
		return slog.LevelDebug
	case "warn", "warning", "wrn":
		return slog.LevelWarn
	case "error", "err", "erro", "fatal":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NormalizeFormat returns "text" or "json".
func NormalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "structured":
		return "json"
	default:
		// text | console | human | pretty | ""
		return "text"
	}
}

// NormalizeColor returns auto | always | never.
func NormalizeColor(color string) string {
	switch strings.ToLower(strings.TrimSpace(color)) {
	case "always", "on", "true", "1", "yes":
		return "always"
	case "never", "off", "false", "0", "no":
		return "never"
	default:
		return "auto"
	}
}

func resolveColor(color string, out io.Writer) bool {
	switch NormalizeColor(color) {
	case "always":
		return true
	case "never":
		return false
	default:
		f, ok := out.(*os.File)
		if !ok {
			return false
		}
		return isTerminal(f)
	}
}
