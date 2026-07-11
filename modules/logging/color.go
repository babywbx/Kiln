package logging

import (
	"log/slog"
	"os"
)

const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
	ansiRed   = "\033[31m"
	ansiYel   = "\033[33m"
	ansiCya   = "\033[36m"
	ansiGra   = "\033[90m"
)

func levelColor(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return ansiRed
	case l >= slog.LevelWarn:
		return ansiYel
	case l >= slog.LevelInfo:
		return ansiCya
	default:
		return ansiGra
	}
}

func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	// NO_COLOR: https://no-color.org/
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
