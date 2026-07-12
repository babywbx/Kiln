package packager

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/egress"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/version"
)

const ffmpegIndexName = "index.m3u8"

// FFmpegAdapter wraps the existing DASH-to-HLS ffmpeg job unchanged. Its
// output, attempt fallback and restart semantics are exactly what they were
// before the seam existed.
type FFmpegAdapter struct {
	cfg       config.FFmpeg
	egress    *proxyegress.Router
	spawn     egress.SpawnGate
	onBytesIn func(int64)
}

func NewFFmpegAdapter(cfg config.FFmpeg, router *proxyegress.Router, spawn egress.SpawnGate, onBytesIn func(int64)) *FFmpegAdapter {
	return &FFmpegAdapter{cfg: cfg, egress: router, spawn: spawn, onBytesIn: onBytesIn}
}

func (a *FFmpegAdapter) Start(ctx context.Context, req Request) (Job, error) {
	prefer := a.cfg.PreferHeight
	if req.PreferHeight > 0 {
		prefer = req.PreferHeight
	}
	job, err := egress.StartDashHLS(ctx, egress.DashOptions{
		Binary:       a.cfg.Binary,
		FFmpegMode:   a.cfg.Mode,
		DockerImage:  a.cfg.DockerImage,
		SourceURL:    req.SourceURL,
		UserAgent:    version.UserAgent(req.UserAgent),
		Headers:      req.Headers,
		Keys:         req.Keys,
		WorkDir:      req.WorkDir,
		HLSTime:      a.cfg.HLSTime,
		HLSListSize:  a.cfg.HLSListSize,
		LogLevel:     a.cfg.LogLevel,
		PreferHeight: prefer,
		LowLatency:   a.cfg.LowLatency,
		Logger:       req.Log,
		OnBytesIn:    a.onBytesIn,
		Egress:       a.egress,
		ChannelID:    req.ChannelID,
		SpawnGate:    a.spawn,
	})
	if err != nil {
		return nil, err
	}
	return &ffmpegJob{job: job, pub: &ffmpegPublication{dir: job.WorkDir()}}, nil
}

type ffmpegJob struct {
	job    *egress.DashJob
	pub    *ffmpegPublication
	reason string
}

func (j *ffmpegJob) Publication() Publication  { return j.pub }
func (j *ffmpegJob) Engine() string            { return EngineFFmpegCopy }
func (j *ffmpegJob) PackMode() string          { return j.job.Mode() }
func (j *ffmpegJob) FallbackReason() string    { return j.reason }
func (j *ffmpegJob) Done() <-chan struct{}     { return j.job.Done() }
func (j *ffmpegJob) Err() error                { return j.job.Err() }
func (j *ffmpegJob) Stop() error               { return j.job.Stop() }
func (j *ffmpegJob) IntentionalStop() bool     { return j.job.IntentionalStop() }
func (j *ffmpegJob) setFallback(reason string) { j.reason = reason }

// ffmpegPublication exposes the ffmpeg work directory as a named playlist plus
// a whitelist of media segments. It does not let a request path address the
// directory directly.
type ffmpegPublication struct {
	dir string
}

func (p *ffmpegPublication) Master() string { return ffmpegIndexName }

func (p *ffmpegPublication) Playlist(name string) ([]byte, bool) {
	if name != ffmpegIndexName {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(p.dir, name))
	if err != nil {
		return nil, false
	}
	return b, true
}

// Asset only resolves the segment names ffmpeg is configured to produce.
// Segment numbers increase monotonically and are never reused, so a published
// segment is immutable.
func (p *ffmpegPublication) Asset(name string) (Asset, bool) {
	if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".ts") {
		return Asset{}, false
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		return Asset{}, false
	}
	path := filepath.Join(p.dir, name)
	st, err := os.Stat(path)
	if err != nil || st.IsDir() {
		return Asset{}, false
	}
	return Asset{Path: path, Immutable: true, ModTime: st.ModTime()}, true
}
