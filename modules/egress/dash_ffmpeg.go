package egress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/proxyegress"
)

const (
	minReadySegBytes   = 32 * 1024
	minReadySegments   = 2
	minReadyEXTINF     = 0.4
	readyTimeoutLocal  = 45 * time.Second
	readyTimeoutRemote = 55 * time.Second
	// 4K / high ladders often need longer first-segment pull through egress.
	readyTimeoutLocal4K  = 75 * time.Second
	readyTimeoutRemote4K = 90 * time.Second
)

type DashJob struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	workDir     string
	cancel      context.CancelFunc
	started     time.Time
	err         error
	done        chan struct{}
	log         *slog.Logger
	intentional bool
	mode        string
	pid         int
	container   string
}

type DashOptions struct {
	Binary       string
	SourceURL    string
	UserAgent    string
	Headers      map[string]string
	Keys         []config.KeyPair
	WorkDir      string
	HLSTime      int
	HLSListSize  int
	LogLevel     string
	PreferHeight int
	LowLatency   bool
	Logger       *slog.Logger
	OnBytesIn    func(n int64)
	Egress       *proxyegress.Router
	ChannelID    string
	FFmpegMode   config.FFmpegMode
	DockerImage  string
}

var kidRe = regexp.MustCompile(`(?i)default_KID="([0-9a-fA-F-]{32,36})"`)
var extinfRe = regexp.MustCompile(`(?i)#EXTINF:([0-9]+(?:\.[0-9]+)?)`)

func StartDashHLS(parent context.Context, opt DashOptions) (*DashJob, error) {
	if err := os.MkdirAll(opt.WorkDir, 0o750); err != nil {
		return nil, err
	}
	if opt.Binary == "" {
		opt.Binary = "ffmpeg"
	}
	if opt.HLSTime <= 0 {
		opt.HLSTime = 2
	}
	if opt.HLSListSize <= 0 {
		if opt.LowLatency {
			opt.HLSListSize = 4
		} else {
			opt.HLSListSize = 6
		}
	}
	if opt.LogLevel == "" {
		opt.LogLevel = "error"
	}
	if len(opt.Keys) == 0 {
		return nil, fmt.Errorf("dash job requires keys")
	}
	log := opt.Logger
	if log == nil {
		log = slog.Default()
	}

	t0 := time.Now()
	resolvedURL, mpdBody, err := resolveMPD(parent, opt)
	if err != nil {
		return nil, err
	}
	mpdMS := time.Since(t0).Milliseconds()
	key := selectKey(opt.Keys, mpdBody)
	if key == "" {
		return nil, fmt.Errorf("no matching decryption key for mpd")
	}

	filtered, note, err := FilterMPDForPack(mpdBody, resolvedURL, opt.PreferHeight)
	if err != nil {
		return nil, err
	}
	pick := PickStreams(mpdBody, opt.PreferHeight)
	absWork, err := filepath.Abs(opt.WorkDir)
	if err != nil {
		return nil, err
	}
	localMPD := filepath.Join(absWork, "input.mpd")
	if err := os.WriteFile(localMPD, []byte(filtered), 0o600); err != nil {
		return nil, err
	}

	local := packAttempt{
		mode:  "local_filtered",
		input: localMPD,
		vMap:  "0:v:0",
		aMap:  "0:a:0?",
		note:  note,
	}
	var attempts []packAttempt
	if pick.Dynamic && resolvedURL != "" {
		aMap := "0:a:0?"
		if pick.AudioIndex >= 0 {
			aMap = fmt.Sprintf("0:a:%d", pick.AudioIndex)
		}
		// Live first: a file: MPD is a snapshot ffmpeg can never refresh, so it
		// starves once the snapshot's timeline runs out. Pin the ladder with -map
		// instead and let ffmpeg poll the manifest itself.
		attempts = append(attempts, packAttempt{
			mode:  "remote_live",
			input: resolvedURL,
			vMap:  fmt.Sprintf("0:v:%d", pick.VideoIndex),
			aMap:  aMap,
			note: fmt.Sprintf("%s map_v=%d map_a=%d id=%s h=%d bw=%d",
				note, pick.VideoIndex, pick.AudioIndex, pick.VideoID, pick.Height, pick.Bandwidth),
			remote: true,
		})
	}
	attempts = append(attempts, local)

	var lastErr error
	for i, att := range attempts {
		cleanHLSArtifacts(absWork)
		if err := os.WriteFile(localMPD, []byte(filtered), 0o600); err != nil {
			return nil, err
		}
		log.Debug("dash ladder selected",
			"mode", att.mode,
			"attempt", i+1,
			"prefer_height", opt.PreferHeight,
			"mpd_ms", mpdMS,
			"detail", att.note,
			"input", redactURL(att.input),
		)
		job, err := startPackager(parent, opt, log, absWork, key, att, resolvedURL)
		if err != nil {
			lastErr = err
			log.Warn("dash packager attempt failed", "mode", att.mode, "err", err)
			continue
		}
		log.Info("dash packager ready",
			"mode", att.mode,
			"mpd_ms", mpdMS,
			"total_ms", time.Since(t0).Milliseconds(),
			"detail", att.note,
		)
		return job, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("dash packager failed")
	}
	return nil, lastErr
}

type packAttempt struct {
	mode   string
	input  string
	vMap   string
	aMap   string
	note   string
	remote bool
}

func cleanHLSArtifacts(work string) {
	entries, err := os.ReadDir(work)
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if name == "index.m3u8" || strings.HasPrefix(name, "seg_") || name == "ffmpeg.stderr.log" {
			_ = os.Remove(filepath.Join(work, name))
		}
	}
}

func startPackager(parent context.Context, opt DashOptions, log *slog.Logger, absWork, key string, att packAttempt, resolvedURL string) (*DashJob, error) {
	indexPath := filepath.Join(absWork, "index.m3u8")
	segPattern := filepath.Join(absWork, "seg_%05d.ts")
	stderrPath := filepath.Join(absWork, "ffmpeg.stderr.log")

	ctx, cancel := context.WithCancel(parent)
	args := []string{
		"-hide_banner",
		"-loglevel", opt.LogLevel,
		"-y",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto,httpproxy",
		"-fflags", "+genpts+discardcorrupt",
	}
	if att.remote {
		args = append(args,
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "5",
		)
		if opt.UserAgent != "" {
			args = append(args, "-user_agent", opt.UserAgent)
		}
		if hdr := formatFFmpegHeaders(opt.Headers); hdr != "" {
			args = append(args, "-headers", hdr)
		}
	}
	args = append(args,
		"-cenc_decryption_key", normalizeKey(key),
		"-i", att.input,
		"-map", att.vMap,
		"-map", att.aMap,
		"-c", "copy",
		"-tag:v", "hvc1",
		"-avoid_negative_ts", "make_zero",
		"-f", "hls",
		"-hls_time", strconv.Itoa(opt.HLSTime),
		"-hls_list_size", strconv.Itoa(opt.HLSListSize),
		"-hls_flags", "delete_segments+append_list+omit_endlist",
		"-hls_segment_filename", segPattern,
		indexPath,
	)

	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ffmpeg stderr log: %w", err)
	}
	var stderrBuf bytes.Buffer
	proxyEnv := []string{}
	if opt.Egress != nil {
		// Segment fetches hit the CDN host from the resolved MPD, not the LAN origin.
		proxyTarget := resolvedURL
		if proxyTarget == "" {
			proxyTarget = opt.SourceURL
		}
		if att.remote && att.input != "" {
			proxyTarget = att.input
		}
		proxyEnv, err = opt.Egress.EnvForFFmpeg(proxyTarget, opt.ChannelID, opt.FFmpegMode.IsDocker())
		if err != nil {
			_ = stderrFile.Close()
			cancel()
			return nil, err
		}
	}
	containerName := ""
	if opt.FFmpegMode.IsDocker() {
		containerName = fmt.Sprintf("kiln-ff-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	plan, err := planFFmpegCommand(opt, absWork, args, proxyEnv, containerName)
	if err != nil {
		_ = stderrFile.Close()
		cancel()
		return nil, err
	}
	cmd := exec.CommandContext(ctx, plan.executable, plan.args...)
	cmd.Stdout = nil
	cmd.Stderr = io.MultiWriter(stderrFile, &stderrBuf)
	configureProcessGroup(cmd)
	cmd.Env = append(os.Environ(), plan.env...)

	job := &DashJob{
		cmd:       cmd,
		workDir:   opt.WorkDir,
		cancel:    cancel,
		started:   time.Now(),
		done:      make(chan struct{}),
		log:       log,
		mode:      att.mode,
		container: plan.containerName,
	}
	if err := cmd.Start(); err != nil {
		_ = stderrFile.Close()
		cancel()
		reapDockerContainer(plan.containerName)
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}
	if cmd.Process != nil {
		job.pid = cmd.Process.Pid
	}
	go func() {
		err := cmd.Wait()
		_ = stderrFile.Close()
		job.mu.Lock()
		job.err = err
		intentional := job.intentional
		job.mu.Unlock()
		if err != nil && !intentional {
			msg := tailLog(&stderrBuf, stderrPath)
			log.Error("ffmpeg exited", "err", err, "mode", att.mode, "stderr", msg)
		}
		reapDockerContainer(plan.containerName)
		close(job.done)
	}()

	timeout := readyTimeoutLocal
	if att.remote {
		timeout = readyTimeoutRemote
	}
	if opt.PreferHeight >= 2160 {
		timeout = readyTimeoutLocal4K
		if att.remote {
			timeout = readyTimeoutRemote4K
		}
	}
	if err := waitPlaylistReady(ctx, job, absWork, timeout, &stderrBuf, stderrPath); err != nil {
		_ = job.Stop()
		return nil, err
	}
	return job, nil
}

func formatFFmpegHeaders(h map[string]string) string {
	if len(h) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range h {
		if k == "" || v == "" || strings.EqualFold(k, "User-Agent") {
			continue
		}
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	return b.String()
}

func redactURL(u string) string {
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, "file:") {
		return u
	}
	if i := strings.Index(u, "?"); i >= 0 {
		return u[:i] + "?…"
	}
	return u
}

func tailLog(buf *bytes.Buffer, path string) string {
	msg := strings.TrimSpace(buf.String())
	if msg == "" {
		if b, err := os.ReadFile(path); err == nil {
			msg = strings.TrimSpace(string(b))
		}
	}
	if len(msg) > 2000 {
		msg = msg[len(msg)-2000:]
	}
	return msg
}

func waitPlaylistReady(ctx context.Context, job *DashJob, workDir string, timeout time.Duration, stderr *bytes.Buffer, stderrPath string) error {
	deadline := time.Now().Add(timeout)
	index := filepath.Join(workDir, "index.m3u8")
	for time.Now().Before(deadline) {
		if readyPlaylist(index, workDir) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-job.done:
			job.mu.Lock()
			err := job.err
			job.mu.Unlock()
			msg := tailLog(stderr, stderrPath)
			if err == nil {
				err = fmt.Errorf("ffmpeg exited before playlist ready")
			}
			if msg != "" {
				return fmt.Errorf("%w: %s", err, msg)
			}
			return err
		case <-time.After(150 * time.Millisecond):
		}
	}
	msg := tailLog(stderr, stderrPath)
	if msg != "" {
		return fmt.Errorf("timeout waiting for hls playlist: %s", msg)
	}
	return fmt.Errorf("timeout waiting for hls playlist")
}

// A single segment only proves ffmpeg started, not that it is still fed: a
// packager reading a stale manifest emits one segment and then starves. Require
// a second one, which only a packager that is actually keeping up can produce.
func readyPlaylist(index, workDir string) bool {
	st, err := os.Stat(index)
	if err != nil || st.Size() == 0 {
		return false
	}
	body, err := os.ReadFile(index)
	if err != nil {
		return false
	}
	listed := 0
	maxDur := 0.0
	for _, m := range extinfRe.FindAllStringSubmatch(string(body), -1) {
		listed++
		if d, err := strconv.ParseFloat(m[1], 64); err == nil && d > maxDur {
			maxDur = d
		}
	}
	if listed < minReadySegments || maxDur < minReadyEXTINF {
		return false
	}
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return false
	}
	var best int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "seg_") || !strings.HasSuffix(name, ".ts") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Size() > best {
			best = info.Size()
		}
	}
	return best >= minReadySegBytes
}

func resolveMPD(ctx context.Context, opt DashOptions) (string, string, error) {
	// Phase 1: hit origin without following (LAN o11 style → 302 to CDN).
	cdnURL, directBody, err := resolveOriginToCDN(ctx, opt)
	if err != nil {
		return "", "", err
	}
	if directBody != "" {
		return cdnURL, directBody, nil
	}

	// Phase 2: fetch CDN MPD with egress; proxied TLS can be flaky so retry.
	var lastErr error
	for attempt := 1; attempt <= 6; attempt++ {
		final, body, err := fetchMPD(ctx, opt, cdnURL)
		if err == nil {
			return final, body, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", "", err
		}
		if attempt < 6 {
			select {
			case <-ctx.Done():
				return "", "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 300 * time.Millisecond):
			}
		}
	}
	return "", "", lastErr
}

func resolveOriginToCDN(ctx context.Context, opt DashOptions) (cdnURL string, body string, err error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if opt.Egress != nil {
		d := opt.Egress.Resolve(opt.SourceURL, opt.ChannelID)
		if hc, e := opt.Egress.ClientForChannel(d.ProxyID, opt.ChannelID, 15*time.Second); e == nil {
			// Keep no-follow redirect policy.
			hc.CheckRedirect = client.CheckRedirect
			hc.Timeout = 15 * time.Second
			client = hc
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opt.SourceURL, nil)
	if err != nil {
		return "", "", err
	}
	applyMPDHeaders(req, opt)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve origin: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if opt.OnBytesIn != nil && len(raw) > 0 {
		opt.OnBytesIn(int64(len(raw)))
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		loc := strings.TrimSpace(resp.Header.Get("Location"))
		if loc == "" {
			return "", "", fmt.Errorf("origin redirect missing location")
		}
		abs, e := url.Parse(loc)
		if e != nil {
			return "", "", fmt.Errorf("origin redirect url: %w", e)
		}
		if !abs.IsAbs() {
			base, e2 := url.Parse(opt.SourceURL)
			if e2 != nil {
				return "", "", e2
			}
			abs = base.ResolveReference(abs)
		}
		return abs.String(), "", nil
	}
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("resolve origin status %s: %s", resp.Status, string(raw[:min(len(raw), 200)]))
	}
	if strings.Contains(string(raw), "<MPD") || strings.Contains(string(raw), "<mpd") {
		final := resp.Request.URL.String()
		return final, string(raw), nil
	}
	return "", "", fmt.Errorf("origin did not return redirect or MPD")
}

// Some CDNs 403 plain http and expect the caller to upgrade the scheme.
func fetchMPD(ctx context.Context, opt DashOptions, mpdURL string) (string, string, error) {
	final, body, err := fetchMPDOnce(ctx, opt, mpdURL)
	if err == nil {
		return final, body, nil
	}
	if u, e := url.Parse(mpdURL); e == nil && u.Scheme == "http" {
		u.Scheme = "https"
		f, b, e2 := fetchMPDOnce(ctx, opt, u.String())
		if e2 == nil {
			return f, b, nil
		}
		return "", "", fmt.Errorf("%w (https retry: %v)", err, e2)
	}
	return "", "", err
}

func fetchMPDOnce(ctx context.Context, opt DashOptions, mpdURL string) (string, string, error) {
	client := &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}
	if opt.Egress != nil {
		d := opt.Egress.Resolve(mpdURL, opt.ChannelID)
		if hc, err := opt.Egress.ClientForChannel(d.ProxyID, opt.ChannelID, 25*time.Second); err == nil {
			hc.CheckRedirect = client.CheckRedirect
			hc.Timeout = 25 * time.Second
			client = hc
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mpdURL, nil)
	if err != nil {
		return "", "", err
	}
	applyMPDHeaders(req, opt)
	resp, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("resolve mpd: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", "", fmt.Errorf("resolve mpd status %s: %s", resp.Status, string(b))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", "", err
	}
	if opt.OnBytesIn != nil && len(body) > 0 {
		opt.OnBytesIn(int64(len(body)))
	}
	final := resp.Request.URL.String()
	if !strings.Contains(string(body), "<MPD") && !strings.Contains(string(body), "<mpd") {
		return "", "", fmt.Errorf("resolved url did not return MPD")
	}
	return final, string(body), nil
}

func applyMPDHeaders(req *http.Request, opt DashOptions) {
	if opt.UserAgent != "" {
		req.Header.Set("User-Agent", opt.UserAgent)
	}
	for k, v := range opt.Headers {
		if k != "" && v != "" {
			req.Header.Set(k, v)
		}
	}
}

func selectKey(keys []config.KeyPair, mpd string) string {
	if len(keys) == 0 {
		return ""
	}
	matches := kidRe.FindAllStringSubmatch(mpd, -1)
	want := map[string]struct{}{}
	for _, m := range matches {
		want[normalizeKid(m[1])] = struct{}{}
	}
	for _, k := range keys {
		if _, ok := want[normalizeKid(k.KID)]; ok {
			return k.Key
		}
	}
	return keys[0].Key
}

func normalizeKid(s string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
}

func (j *DashJob) WorkDir() string       { return j.workDir }
func (j *DashJob) Done() <-chan struct{} { return j.done }
func (j *DashJob) Mode() string {
	if j == nil {
		return ""
	}
	return j.mode
}
func (j *DashJob) IntentionalStop() bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.intentional
}

func (j *DashJob) Stop() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	j.intentional = true
	pid := j.pid
	if j.cmd != nil && j.cmd.Process != nil {
		pid = j.cmd.Process.Pid
	}
	containerName := j.container
	j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
	if pid > 0 {
		_ = terminateProcessGroup(pid, false)
	}
	select {
	case <-j.done:
		return nil
	case <-time.After(2 * time.Second):
	}
	if pid > 0 {
		_ = terminateProcessGroup(pid, true)
	}
	if j.cmd != nil && j.cmd.Process != nil {
		_ = j.cmd.Process.Kill()
	}
	select {
	case <-j.done:
		return nil
	case <-time.After(2 * time.Second):
	}
	reapDockerContainer(containerName)
	return nil
}

func reapDockerContainer(name string) {
	if name == "" || strings.ContainsAny(name, " \t\n/\\") {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", name).Run()
}

func (j *DashJob) Err() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.err
}

func normalizeKey(key string) string {
	k := strings.TrimSpace(strings.ToLower(key))
	return strings.TrimPrefix(k, "0x")
}
