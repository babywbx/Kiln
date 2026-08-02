package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

type Options struct {
	Level  string
	Format string
	Color  string
	Output io.Writer
}

func New(level, format string) *slog.Logger {
	return NewWith(Options{Level: level, Format: format, Color: "auto"})
}

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

func Bootstrap() *slog.Logger {
	return NewWith(Options{Level: "info", Format: "text", Color: "auto"})
}

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

func NormalizeFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json", "structured":
		return "json"
	default:
		return "text"
	}
}

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
