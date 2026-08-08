package packager

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/egress"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/version"
)

const ffmpegIndexName = "index.m3u8"

func CheckFFmpegDependency(cfg config.FFmpeg) error {
	dependency := strings.TrimSpace(cfg.Dependency())
	if dependency == "" {
		return fmt.Errorf("ffmpeg dependency is not configured")
	}
	if _, err := exec.LookPath(dependency); err != nil {
		return fmt.Errorf("find ffmpeg dependency %q: %w", dependency, err)
	}
	return nil
}

type FFmpegAdapter struct {
	cfg    config.FFmpeg
	egress *proxyegress.Router
	pull   *pull.Client
	spawn  egress.SpawnGate
}

func NewFFmpegAdapter(
	cfg config.FFmpeg,
	pullClient *pull.Client,
	router *proxyegress.Router,
	spawn egress.SpawnGate,
) *FFmpegAdapter {
	return &FFmpegAdapter{cfg: cfg, egress: router, pull: pullClient, spawn: spawn}
}

func (a *FFmpegAdapter) Start(ctx context.Context, req Request) (Job, error) {
	prefer := a.cfg.PreferHeight
	if req.PreferHeight > 0 {
		prefer = req.PreferHeight
	}
	job, err := egress.StartDashHLS(ctx, egress.DashOptions{
		Binary:                a.cfg.Binary,
		FFmpegMode:            a.cfg.Mode,
		DockerImage:           a.cfg.DockerImage,
		SourceURL:             req.SourceURL,
		UserAgent:             version.UserAgent(req.UserAgent),
		Headers:               req.Headers,
		Keys:                  req.Keys,
		WorkDir:               req.WorkDir,
		HLSTime:               a.cfg.HLSTime,
		HLSListSize:           a.cfg.HLSListSize,
		LogLevel:              a.cfg.LogLevel,
		PreferHeight:          prefer,
		VideoRepresentationID: req.Selection.Video.Track.RepresentationID,
		AudioRepresentationID: req.Selection.Audio.Track.RepresentationID,
		LowLatency:            a.cfg.LowLatency,
		Logger:                req.Log,
		Egress:                a.egress,
		Pull:                  a.pull,
		ChannelID:             req.ChannelID,
		SpawnGate:             a.spawn,
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

func (j *ffmpegJob) Stats() Stats { return Stats{} }

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
