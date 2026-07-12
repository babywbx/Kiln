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
	Stats() Stats
}

// Stats is what a running job reports about itself. These are plain fields on
// the existing status snapshot, not a new metrics endpoint: adding one would
// mean a new dependency and a new unauthenticated surface.
type Stats struct {
	SegmentsPublished uint64  `json:"segments_published,omitempty"`
	SegmentsFetched   uint64  `json:"segments_fetched,omitempty"`
	SegmentFetchErrs  uint64  `json:"segment_fetch_errors,omitempty"`
	ManifestRefreshes uint64  `json:"manifest_refreshes,omitempty"`
	ManifestErrs      uint64  `json:"manifest_errors,omitempty"`
	Discontinuities   uint64  `json:"discontinuities,omitempty"`
	Reanchors         uint64  `json:"reanchors,omitempty"`
	KeyMismatches     uint64  `json:"key_mismatches,omitempty"`
	DecryptSeconds    float64 `json:"decrypt_seconds,omitempty"`
	CacheBytes        int64   `json:"cache_bytes,omitempty"`
	CacheItems        int     `json:"cache_items,omitempty"`
	// VideoFrontier and AudioFrontier are the highest published sequence per
	// track. They only ever move forward.
	VideoFrontier uint64 `json:"video_frontier,omitempty"`
	AudioFrontier uint64 `json:"audio_frontier,omitempty"`
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
