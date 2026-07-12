package packager

import (
	"context"
	"log/slog"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

// Request is everything a packager needs to serve one channel. It carries no
// ffmpeg, Docker or filesystem concepts: those belong to the adapters.
type Request struct {
	ChannelID    string
	SourceURL    string
	Keys         []config.KeyPair
	Headers      map[string]string
	UserAgent    string
	WorkDir      string
	PreferHeight int
	Engine       string
	Log          *slog.Logger
}

// Engine strategies a channel may ask for.
const (
	StrategyAuto   = "auto"
	StrategyNative = "native"
	StrategyFFmpeg = "ffmpeg"
)

func ValidStrategy(s string) bool {
	switch s {
	case StrategyAuto, StrategyNative, StrategyFFmpeg:
		return true
	}
	return false
}

type Packager interface {
	Start(ctx context.Context, req Request) (Job, error)
}

type Job interface {
	Publication() Publication
	// Engine is the resolved engine, e.g. native_rewrite or ffmpeg_copy.
	Engine() string
	// PackMode is the engine's internal mode. ffmpeg keeps its existing
	// remote_live / local_filtered values; they are not the same axis as Engine.
	PackMode() string
	// FallbackReason is set when auto mode declined the native path.
	FallbackReason() string
	Done() <-chan struct{}
	Err() error
	Stop() error
	// IntentionalStop separates "we stopped it" from "it died". The restart
	// budget depends on it, so it is not optional.
	IntentionalStop() bool
}

// Publication is the only way the HTTP layer sees media. It hands out named
// playlists and named assets; it never exposes a directory to walk.
type Publication interface {
	// Master is the playlist a player starts from.
	Master() string
	Playlist(name string) ([]byte, bool)
	Asset(name string) (Asset, bool)
}

// Asset is a published, complete file. Immutable assets may be cached by the
// player; playlists never are.
type Asset struct {
	Path      string
	Immutable bool
	ModTime   time.Time
}
