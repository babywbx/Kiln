package packager

import (
	"context"
	"log/slog"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

type Request struct {
	ChannelID               string
	SourceURL               string
	Keys                    []config.KeyPair
	Headers                 map[string]string
	UserAgent               string
	WorkDir                 string
	PreferHeight            int
	PreferredAudioLanguages []string
	Engine                  string
	Log                     *slog.Logger
}

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

	Engine() string

	PackMode() string

	FallbackReason() string
	Done() <-chan struct{}
	Err() error
	Stop() error

	IntentionalStop() bool
	Stats() Stats
}

type Stats struct {
	SegmentsPublished uint64 `json:"segments_published,omitempty"`
	PartsPublished    uint64 `json:"parts_published,omitempty"`
	SegmentsFetched   uint64 `json:"segments_fetched,omitempty"`
	SegmentFetchErrs  uint64 `json:"segment_fetch_errors,omitempty"`
	ManifestRefreshes uint64 `json:"manifest_refreshes,omitempty"`
	ManifestErrs      uint64 `json:"manifest_errors,omitempty"`
	Discontinuities   uint64 `json:"discontinuities,omitempty"`
	Reanchors         uint64 `json:"reanchors,omitempty"`

	Reresolves uint64 `json:"reresolves,omitempty"`

	TrackHolds     uint64  `json:"track_holds,omitempty"`
	KeyMismatches  uint64  `json:"key_mismatches,omitempty"`
	DecryptSeconds float64 `json:"decrypt_seconds,omitempty"`
	CacheBytes     int64   `json:"cache_bytes,omitempty"`
	CacheItems     int     `json:"cache_items,omitempty"`

	VideoFrontier uint64 `json:"video_frontier,omitempty"`
	AudioFrontier uint64 `json:"audio_frontier,omitempty"`

	VideoTracks        int     `json:"video_tracks,omitempty"`
	AudioTracks        int     `json:"audio_tracks,omitempty"`
	TextTracks         int     `json:"text_tracks,omitempty"`
	ClockOffsetSeconds float64 `json:"clock_offset_seconds,omitempty"`
}

type Publication interface {
	Master() string
	Playlist(name string) ([]byte, bool)
	Asset(name string) (Asset, bool)
}

type PlaylistRequest struct {
	Skip bool
	MSN  *uint64
	Part *uint64
}

type PlaylistView struct {
	Body     []byte
	Revision uint64
}

// ContextPublication is implemented by native LL-HLS publications. Keeping it
// optional preserves the same HTTP seam for ffmpeg and passthrough engines.
type ContextPublication interface {
	PlaylistContext(context.Context, string, PlaylistRequest) (PlaylistView, bool, error)
	AssetContext(context.Context, string) (Asset, bool, error)
}

type Asset struct {
	Path      string
	Immutable bool
	ModTime   time.Time
}
