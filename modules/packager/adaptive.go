package packager

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/packager/cmaf"
	"github.com/babywbx/kiln/modules/packager/hls"
)

type NativeAdapter struct {
	StartSegments    int
	Prefetch         int
	MaxSegmentBytes  int64
	PrimaryTrackHold time.Duration

	newFetcher   func(req Request) Fetcher
	playlistSize int
	grace        time.Duration
	now          func() time.Time

	gate *byteGate
}

func NewNativeAdapter(newFetcher func(req Request) Fetcher, playlistSize int, grace time.Duration) *NativeAdapter {
	return &NativeAdapter{
		newFetcher:   newFetcher,
		playlistSize: playlistSize,
		grace:        grace,
		gate:         newByteGate(defaultInflightBytes),
	}
}

func (a *NativeAdapter) SetInflightBytes(limit int64) {
	a.gate = newByteGate(limit)
}

func (a *NativeAdapter) Start(ctx context.Context, req Request) (Job, error) {
	keys, err := keySet(req.Keys)
	if err != nil {
		return nil, &FallbackError{Reason: ReasonMissingKey, Allowed: true, Err: err}
	}
	native, err := StartNative(ctx, Options{
		ManifestURL:      req.SourceURL,
		Dir:              req.WorkDir,
		Keys:             keys,
		Fetcher:          a.newFetcher(req),
		PreferHeight:     req.PreferHeight,
		PlaylistSize:     a.playlistSize,
		StartSegments:    a.StartSegments,
		Prefetch:         a.Prefetch,
		MaxSegmentBytes:  a.MaxSegmentBytes,
		PrimaryTrackHold: a.PrimaryTrackHold,
		Gate:             a.gate,
		Grace:            a.grace,
		Now:              a.now,
		Log:              req.Log,
	})
	if err != nil {
		return nil, err
	}
	return &nativeJob{native: native, pub: &nativePublication{pub: native.Publication(), dir: req.WorkDir}}, nil
}

func keySet(pairs []config.KeyPair) (cmaf.KeySet, error) {
	if len(pairs) == 0 {
		return nil, errors.New("no decryption keys configured")
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		m[p.KID] = p.Key
	}
	return cmaf.NewKeySet(m)
}

type nativeJob struct {
	native *Native
	pub    *nativePublication
}

func (j *nativeJob) Publication() Publication { return j.pub }
func (j *nativeJob) Engine() string           { return j.native.Engine() }
func (j *nativeJob) PackMode() string         { return j.native.PackMode() }
func (j *nativeJob) FallbackReason() string   { return "" }
func (j *nativeJob) Done() <-chan struct{}    { return j.native.Done() }
func (j *nativeJob) Err() error               { return j.native.Err() }
func (j *nativeJob) Stop() error              { return j.native.Stop() }
func (j *nativeJob) IntentionalStop() bool    { return j.native.IntentionalStop() }
func (j *nativeJob) Stats() Stats             { return j.native.Stats() }

type nativePublication struct {
	pub *hls.Publisher
	dir string
}

func (p *nativePublication) Master() string { return hls.MasterName }

func (p *nativePublication) Playlist(name string) ([]byte, bool) {
	if !strings.HasSuffix(name, ".m3u8") {
		return nil, false
	}
	return p.pub.Playlist(name)
}

func (p *nativePublication) Asset(name string) (Asset, bool) {
	path, ok := p.pub.Asset(name)
	if !ok {
		return Asset{}, false
	}
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return Asset{}, false
	}
	return Asset{Path: path, Immutable: true, ModTime: st.ModTime()}, true
}

type AdaptivePackager struct {
	native *NativeAdapter
	ffmpeg Packager
	log    *slog.Logger
}

func NewAdaptivePackager(native *NativeAdapter, ffmpeg Packager, log *slog.Logger) *AdaptivePackager {
	if log == nil {
		log = slog.Default()
	}
	return &AdaptivePackager{native: native, ffmpeg: ffmpeg, log: log}
}

func (a *AdaptivePackager) Start(ctx context.Context, req Request) (Job, error) {
	strategy := req.Engine
	if strategy == "" {
		strategy = StrategyAuto
	}
	if !ValidStrategy(strategy) {
		return nil, apperr.New(apperr.CodeInvalid, 400, fmt.Sprintf("unknown engine %q", strategy))
	}

	if strategy == StrategyFFmpeg {
		return a.startFFmpeg(ctx, req, "")
	}

	job, err := a.native.Start(ctx, cleanWorkDir(req, "native"))
	if err == nil {
		return job, nil
	}

	if ctx.Err() != nil {
		return nil, err
	}

	reason := ReasonNativeStartFailed
	var fb *FallbackError
	if errors.As(err, &fb) {
		reason = fb.Reason
		if !fb.Allowed {

			return nil, apperr.Wrap(apperr.CodeUpstream, 502,
				"native cannot handle this source and ffmpeg would decode it incorrectly", err)
		}
	}
	if strategy == StrategyNative {
		return nil, apperr.Wrap(apperr.CodeUpstream, 502,
			"engine=native but the source cannot be served natively", err)
	}

	a.log.Info("falling back to ffmpeg", "channel", req.ChannelID, "reason", reason, "err", err)
	return a.startFFmpeg(ctx, req, reason)
}

func (a *AdaptivePackager) startFFmpeg(ctx context.Context, req Request, reason string) (Job, error) {
	job, err := a.ffmpeg.Start(ctx, cleanWorkDir(req, "ffmpeg"))
	if err != nil {
		return nil, err
	}
	if fj, ok := job.(*ffmpegJob); ok {
		fj.setFallback(reason)
	}
	return job, nil
}

func cleanWorkDir(req Request, engine string) Request {
	out := req
	out.WorkDir = filepath.Join(req.WorkDir, engine)
	_ = os.RemoveAll(out.WorkDir)
	return out
}
