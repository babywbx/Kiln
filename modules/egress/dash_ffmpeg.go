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
	"syscall"
	"time"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/proxyegress"
)

const (
	minReadySegBytes   = 32 * 1024
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
	DockerFFmpeg bool
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

	// Local filtered first (single video+audio). For live, FilterMPD keeps the full
	// SegmentTimeline so first-segment times stay valid; trim caused HTTP 410 on 4K.
	attempts := []packAttempt{{
		mode:  "local_filtered",
		input: localMPD,
		vMap:  "0:v:0",
		aMap:  "0:a:0?",
		note:  note,
	}}
	if pick.Dynamic && resolvedURL != "" {
		aMap := "0:a:0?"
		if pick.AudioIndex >= 0 {
			aMap = fmt.Sprintf("0:a:%d", pick.AudioIndex)
		}
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
	nameFile := filepath.Join(absWork, ".ffmpeg-container")

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
	cmd := exec.CommandContext(ctx, opt.Binary, args...)
	cmd.Stdout = nil
	cmd.Stderr = io.MultiWriter(stderrFile, &stderrBuf)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := append([]string{}, os.Environ()...)
	env = append(env, "KILN_FF_NAME_FILE="+nameFile)
	if opt.Egress != nil {
		forDocker := opt.DockerFFmpeg || looksLikeDockerFFmpeg(opt.Binary)
		// Segment fetches hit the CDN host from the resolved MPD, not the LAN origin.
		proxyTarget := resolvedURL
		if proxyTarget == "" {
			proxyTarget = opt.SourceURL
		}
		if att.remote && att.input != "" {
			proxyTarget = att.input
		}
		if proxyEnv := opt.Egress.EnvForFFmpeg(proxyTarget, opt.ChannelID, forDocker); len(proxyEnv) > 0 {
			env = append(env, proxyEnv...)
		}
	}
	cmd.Env = env

	job := &DashJob{
		cmd:     cmd,
		workDir: opt.WorkDir,
		cancel:  cancel,
		started: time.Now(),
		done:    make(chan struct{}),
		log:     log,
		mode:    att.mode,
	}
	if err := cmd.Start(); err != nil {
		_ = stderrFile.Close()
		cancel()
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

func readyPlaylist(index, workDir string) bool {
	st, err := os.Stat(index)
	if err != nil || st.Size() == 0 {
		return false
	}
	body, err := os.ReadFile(index)
	if err != nil {
		return false
	}
	maxDur := 0.0
	for _, m := range extinfRe.FindAllStringSubmatch(string(body), -1) {
		if d, err := strconv.ParseFloat(m[1], 64); err == nil && d > maxDur {
			maxDur = d
		}
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
	if best >= minReadySegBytes && maxDur >= minReadyEXTINF {
		return true
	}
	if best >= 200*1024 {
		return true
	}
	return false
}

func looksLikeDockerFFmpeg(binary string) bool {
	b := strings.ToLower(binary)
	return strings.Contains(b, "ffmpeg-cenc") || strings.Contains(b, "docker")
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
		forceHTTPSForUpstream(abs)
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

func fetchMPD(ctx context.Context, opt DashOptions, mpdURL string) (string, string, error) {
	client := &http.Client{
		Timeout: 25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			forceHTTPSForUpstream(req.URL)
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
	if strings.HasPrefix(final, "https://") && strings.Contains(final, ":80/") {
		final = "http://" + strings.TrimPrefix(final, "https://")
	}
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

func forceHTTPSForUpstream(u *url.URL) {
	if u == nil || u.Scheme != "http" {
		return
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, "origin.example.com") || strings.HasSuffix(host, ".example.com") {
		u.Scheme = "https"
		if p := u.Port(); p == "80" || p == "443" {
			u.Host = u.Hostname()
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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
	workDir := j.workDir
	j.mu.Unlock()
	if j.cancel != nil {
		j.cancel()
	}
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
	}
	select {
	case <-j.done:
		reapDockerFFmpeg(workDir)
		return nil
	case <-time.After(2 * time.Second):
	}
	if pid > 0 {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	if j.cmd != nil && j.cmd.Process != nil {
		_ = j.cmd.Process.Kill()
	}
	select {
	case <-j.done:
	case <-time.After(2 * time.Second):
	}
	reapDockerFFmpeg(workDir)
	return nil
}

func reapDockerFFmpeg(workDir string) {
	if workDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(workDir, ".ffmpeg-container"))
	if err != nil {
		return
	}
	name := strings.TrimSpace(string(b))
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
