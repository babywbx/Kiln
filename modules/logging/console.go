package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Modern compact console format:
//
//	2026-07-11 15:04:05  INFO   kiln starting  version=0.1.0 channels=3
//	2026-07-11 15:04:05  INFO   [channel-1] session started  ingress=hls
//	2026-07-11 15:04:05  ERROR  [channel-1] session restart failed  err="connection refused"
//
// Time is local wall clock. Levels are 3 letters. Fields are logfmt key=value.
type consoleHandler struct {
	mu    *sync.Mutex
	w     io.Writer
	opts  slog.HandlerOptions
	attrs []slog.Attr
	group string
	color bool
}

func newConsoleHandler(w io.Writer, opts *slog.HandlerOptions, color bool) *consoleHandler {
	h := &consoleHandler{mu: &sync.Mutex{}, w: w, color: color}
	if opts != nil {
		h.opts = *opts
	}
	if h.opts.Level == nil {
		h.opts.Level = slog.LevelInfo
	}
	return h
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.opts.Level.Level()
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	cp := *h
	cp.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &cp
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	cp := *h
	if h.group != "" {
		cp.group = h.group + "." + name
	} else {
		cp.group = name
	}
	cp.attrs = append([]slog.Attr{}, h.attrs...)
	return &cp
}

func (h *consoleHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.Grow(192)

	ts := r.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	// time
	b.WriteString(ts.Format("2006-01-02 15:04:05"))
	b.WriteString("  ")

	// level
	lvl := levelTag(r.Level)
	if h.color {
		b.WriteString(levelColor(r.Level))
		b.WriteString(lvl)
		b.WriteString(ansiReset)
	} else {
		b.WriteString(lvl)
	}
	b.WriteString("  ")

	// collect attrs
	attrs := make([]slog.Attr, 0, len(h.attrs)+8)
	attrs = append(attrs, h.attrs...)
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, a)
		return true
	})
	kv := flatten(attrs, h.group)
	delete(kv, "service")

	// optional [channel] scope — scannable in media gateways
	if ch := take(kv, "channel"); ch != "" {
		if h.color {
			b.WriteString(ansiDim)
		}
		b.WriteByte('[')
		b.WriteString(ch)
		b.WriteByte(']')
		if h.color {
			b.WriteString(ansiReset)
		}
		b.WriteByte(' ')
	}

	// message
	if h.color {
		b.WriteString(ansiBold)
	}
	b.WriteString(strings.TrimSpace(r.Message))
	if h.color {
		b.WriteString(ansiReset)
	}

	// fields in stable preferred order
	writeFields(&b, kv, h.color)

	if h.opts.AddSource && r.PC != 0 {
		fs := runtime.CallersFrames([]uintptr{r.PC})
		f, _ := fs.Next()
		if f.File != "" {
			b.WriteByte(' ')
			if h.color {
				b.WriteString(ansiDim)
			}
			b.WriteString(shortSource(f.File))
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(f.Line))
			if h.color {
				b.WriteString(ansiReset)
			}
		}
	}

	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

// Fixed width 5 so INFO/WARN align with ERROR/DEBUG.
func levelTag(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN "
	case l >= slog.LevelInfo:
		return "INFO "
	default:
		return "DEBUG"
	}
}

// Preferred field order for common keys; remaining keys sorted alphabetically.
var fieldOrder = []string{
	"remote", "method", "path", "status", "dur_ms", "request_id",
	"ingress", "pack_mode", "mode", "attempt", "restarts", "delay", "reason",
	"addr", "listen", "version", "channels", "config", "db", "admin",
	"proxies", "egress_default", "playlist_policy",
	"detail", "prefer_height", "input", "mpd_ms", "total_ms",
	"err", "error", "panic", "stderr", "signal",
}

func writeFields(b *strings.Builder, kv map[string]string, color bool) {
	if len(kv) == 0 {
		return
	}
	// two spaces between message and first field (visual column)
	b.WriteString("  ")
	first := true
	emit := func(k, v string) {
		if !first {
			b.WriteByte(' ')
		}
		first = false
		writeField(b, k, v, color)
	}
	for _, k := range fieldOrder {
		v, ok := kv[k]
		if !ok {
			continue
		}
		delete(kv, k)
		emit(k, v)
	}
	for _, k := range sortedKeys(kv) {
		emit(k, kv[k])
	}
}

func writeField(b *strings.Builder, key, val string, color bool) {
	if color {
		b.WriteString(ansiDim)
	}
	b.WriteString(key)
	b.WriteByte('=')
	if color {
		b.WriteString(ansiReset)
	}
	if color && (key == "err" || key == "error" || key == "panic") {
		b.WriteString(ansiRed)
		b.WriteString(quoteIfNeeded(val))
		b.WriteString(ansiReset)
		return
	}
	b.WriteString(quoteIfNeeded(val))
}

func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	for _, r := range s {
		if r <= ' ' || r == '=' || r == '"' || r == '\\' {
			return strconv.Quote(s)
		}
	}
	return s
}

func flatten(attrs []slog.Attr, group string) map[string]string {
	out := make(map[string]string, len(attrs))
	var walk func(prefix string, a slog.Attr)
	walk = func(prefix string, a slog.Attr) {
		a.Value = a.Value.Resolve()
		key := a.Key
		if prefix != "" {
			key = prefix + "." + key
		}
		if a.Value.Kind() == slog.KindGroup {
			for _, ga := range a.Value.Group() {
				walk(key, ga)
			}
			return
		}
		if key == "" {
			return
		}
		out[key] = valueString(a.Value)
	}
	prefix := group
	for _, a := range attrs {
		walk(prefix, a)
	}
	return out
}

func valueString(v slog.Value) string {
	switch v.Kind() {
	case slog.KindString:
		return v.String()
	case slog.KindInt64:
		return strconv.FormatInt(v.Int64(), 10)
	case slog.KindUint64:
		return strconv.FormatUint(v.Uint64(), 10)
	case slog.KindFloat64:
		return strconv.FormatFloat(v.Float64(), 'f', -1, 64)
	case slog.KindBool:
		if v.Bool() {
			return "true"
		}
		return "false"
	case slog.KindDuration:
		return v.Duration().String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339)
	case slog.KindAny:
		if err, ok := v.Any().(error); ok && err != nil {
			return err.Error()
		}
		return fmt.Sprint(v.Any())
	default:
		return v.String()
	}
}

func take(kv map[string]string, key string) string {
	v, ok := kv[key]
	if !ok {
		return ""
	}
	delete(kv, key)
	return v
}

func sortedKeys(kv map[string]string) []string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		j := i
		for j > 0 && keys[j-1] > keys[j] {
			keys[j-1], keys[j] = keys[j], keys[j-1]
			j--
		}
	}
	return keys
}

func shortSource(file string) string {
	// keep last two path segments
	n := 0
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '/' {
			n++
			if n == 2 {
				return file[i+1:]
			}
		}
	}
	return file
}
