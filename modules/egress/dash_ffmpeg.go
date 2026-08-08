package egress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/babywbx/kiln/modules/pull"
)

const (
	minReadySegBytes     = 32 * 1024
	minReadySegments     = 2
	minReadyEXTINF       = 0.4
	readyTimeoutLocal    = 45 * time.Second
	readyTimeoutRemote   = 55 * time.Second
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

type SpawnGate interface {
	Acquire(ctx context.Context) (release func(), err error)
}

type DashOptions struct {
	Binary                string
	SourceURL             string
	UserAgent             string
	Headers               map[string]string
	Keys                  []config.KeyPair
	WorkDir               string
	HLSTime               int
	HLSListSize           int
	LogLevel              string
	PreferHeight          int
	VideoRepresentationID string
	AudioRepresentationID string
	LowLatency            bool
	Logger                *slog.Logger
	Egress                *proxyegress.Router
	Pull                  *pull.Client
	ChannelID             string
	FFmpegMode            config.FFmpegMode
	DockerImage           string
	SpawnGate             SpawnGate
}

var kidRe = regexp.MustCompile(`(?i)default_KID="([0-9a-fA-F-]{32,36})"`)
var extinfRe = regexp.MustCompile(`(?i)#EXTINF:([0-9]+(?:\.[0-9]+)?)`)
var logURLRe = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

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
			opt.HLSListSize = 8
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

	filtered, note, err := FilterMPDForPackWithSelection(mpdBody, resolvedURL, opt.PreferHeight, opt.VideoRepresentationID, opt.AudioRepresentationID)
	if err != nil {
		return nil, err
	}
	ffmpegHeaders := opt.Headers
	if !sameURLOrigin(resolvedURL, opt.SourceURL) {
		ffmpegHeaders = nil
	}
	if err := validateFFmpegMPD(filtered, resolvedURL, opt.Pull, ffmpegHeaders); err != nil {
		return nil, err
	}
	absWork, err := filepath.Abs(opt.WorkDir)
	if err != nil {
		return nil, err
	}
	localMPD := filepath.Join(absWork, "input.mpd")
	if err := writeFFmpegMPD(localMPD, []byte(filtered)); err != nil {
		return nil, err
	}

	local := packAttempt{
		mode:            "local_filtered",
		input:           localMPD,
		vMap:            "0:v:0",
		aMap:            "0:a:0?",
		note:            note,
		network:         opt.Pull != nil,
		headers:         ffmpegHeaders,
		refreshInterval: ffmpegMPDRefreshInterval(filtered),
	}
	attempts := []packAttempt{local}

	var lastErr error
	for i, att := range attempts {
		cleanHLSArtifacts(absWork)
		if err := writeFFmpegMPD(localMPD, []byte(filtered)); err != nil {
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
	mode            string
	input           string
	vMap            string
	aMap            string
	note            string
	network         bool
	headers         map[string]string
	proxyURL        string
	refreshInterval time.Duration
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
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("open ffmpeg stderr log: %w", err)
	}
	var stderrBuf bytes.Buffer
	var forwardProxy *ffmpegForwardProxy
	proxyEnv := []string(nil)
	if att.network {
		forwardProxy, err = startFFmpegForwardProxy(ffmpegProxyOptions{
			Client:       opt.Pull,
			ChannelID:    opt.ChannelID,
			HeaderOrigin: resolvedURL,
			Headers:      att.headers,
			UserAgent:    opt.UserAgent,
			Docker:       opt.FFmpegMode.IsDocker(),
		})
		if err != nil {
			_ = stderrFile.Close()
			cancel()
			return nil, err
		}
		att.proxyURL = forwardProxy.URL()
		proxyEnv = forwardProxy.Env()
	}
	closeProxy := func() {
		if forwardProxy != nil {
			forwardProxy.Close()
		}
	}
	args := buildPackagerArgs(opt, att, key, indexPath, segPattern)
	containerName := ""
	if opt.FFmpegMode.IsDocker() {
		containerName = fmt.Sprintf("kiln-ff-%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	plan, err := planFFmpegCommand(opt, absWork, args, proxyEnv, containerName)
	if err != nil {
		_ = stderrFile.Close()
		cancel()
		closeProxy()
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
	if err := spawn(ctx, opt.SpawnGate, cmd); err != nil {
		_ = stderrFile.Close()
		cancel()
		closeProxy()
		reapDockerContainer(plan.containerName)
		return nil, err
	}
	if cmd.Process != nil {
		job.pid = cmd.Process.Pid
	}
	if att.refreshInterval > 0 {
		go refreshFFmpegMPD(ctx, opt, att.input, resolvedURL, att.headers, att.refreshInterval, log)
	}
	go func() {
		processErr := cmd.Wait()
		cancel()
		_ = stderrFile.Close()
		job.mu.Lock()
		if job.err == nil {
			job.err = processErr
		}
		jobErr := job.err
		intentional := job.intentional
		job.mu.Unlock()
		if jobErr != nil && !intentional {
			msg := tailLog(&stderrBuf, stderrPath)
			log.Error("ffmpeg exited", "err", jobErr, "mode", att.mode, "stderr", msg)
		}
		reapDockerContainer(plan.containerName)
		closeProxy()
		close(job.done)
	}()

	timeout := readyTimeoutLocal
	if att.network {
		timeout = readyTimeoutRemote
	}
	if opt.PreferHeight >= 2160 {
		timeout = readyTimeoutLocal4K
		if att.network {
			timeout = readyTimeoutRemote4K
		}
	}
	if err := waitPlaylistReady(ctx, job, absWork, timeout, &stderrBuf, stderrPath); err != nil {
		_ = job.Stop()
		return nil, err
	}
	startPlaylistWatchdog(ctx, job, indexPath, playlistWatchInterval(opt.HLSTime), playlistStallTimeout(opt.HLSTime, opt.HLSListSize))
	return job, nil
}

func buildPackagerArgs(opt DashOptions, att packAttempt, key, indexPath, segPattern string) []string {
	protocols := "file,crypto"
	if att.network {
		protocols = "file,http,https,tcp,tls,crypto,httpproxy"
	}
	args := []string{
		"-hide_banner",
		"-loglevel", opt.LogLevel,
		"-y",
		"-protocol_whitelist", protocols,
		"-fflags", "+genpts+discardcorrupt",
	}
	if att.network {
		args = append(args,
			"-max_redirects", "0",
			"-reconnect", "1",
			"-reconnect_streamed", "1",
			"-reconnect_delay_max", "5",
		)
		if att.proxyURL != "" {
			args = append(args, "-http_proxy", att.proxyURL)
		}
		if opt.UserAgent != "" {
			args = append(args, "-user_agent", opt.UserAgent)
		}
		if source, err := url.Parse(opt.SourceURL); err == nil && strings.EqualFold(source.Scheme, "https") {
			if headers := formatFFmpegHeaders(att.headers); headers != "" {
				args = append(args, "-headers", headers)
			}
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
		"-hls_start_number_source", "epoch_us",
		"-hls_segment_filename", segPattern,
		indexPath,
	)
	return args
}

func spawn(ctx context.Context, gate SpawnGate, cmd *exec.Cmd) error {
	if gate != nil {
		release, err := gate.Acquire(ctx)
		if err != nil {
			return err
		}
		defer release()
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	return nil
}

func hasFFmpegCustomHeaders(headers map[string]string) bool {
	for name, value := range headers {
		if name != "" && value != "" && !strings.EqualFold(name, "User-Agent") {
			return true
		}
	}
	return false
}

func formatFFmpegHeaders(headers map[string]string) string {
	var b strings.Builder
	for name, value := range headers {
		if name == "" || value == "" || strings.EqualFold(name, "User-Agent") {
			continue
		}
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\r\n")
	}
	return b.String()
}

func remoteNetworkSource(rawURL string) bool {
	if filepath.IsAbs(rawURL) {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	return u.Host != "" || u.Scheme != "" && !strings.EqualFold(u.Scheme, "file")
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

func redactLogError(err error) string {
	if err == nil {
		return ""
	}
	return logURLRe.ReplaceAllStringFunc(err.Error(), func(raw string) string {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.Scheme == "" || u.Hostname() == "" {
			return "<redacted-url>"
		}
		return (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	})
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

type playlistProgress struct {
	mediaSequence uint64
	hasSequence   bool
	latestSegment string
}

func startPlaylistWatchdog(ctx context.Context, job *DashJob, indexPath string, pollInterval, stallTimeout time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := watchPlaylistProgressUntil(ctx, job.done, indexPath, pollInterval, stallTimeout)
		if err != nil {
			job.fail(err)
		}
	}()
	return done
}

func watchPlaylistProgress(ctx context.Context, indexPath string, pollInterval, stallTimeout time.Duration) error {
	return watchPlaylistProgressUntil(ctx, nil, indexPath, pollInterval, stallTimeout)
}

func watchPlaylistProgressUntil(ctx context.Context, jobDone <-chan struct{}, indexPath string, pollInterval, stallTimeout time.Duration) error {
	lastProgress := time.Now()
	progress, haveProgress := readPlaylistProgress(indexPath)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-jobDone:
			return nil
		case now := <-ticker.C:
			current, ok := readPlaylistProgress(indexPath)
			if ok && (!haveProgress || current != progress) {
				progress = current
				haveProgress = true
				lastProgress = now
			}
			if now.Sub(lastProgress) >= stallTimeout {
				return fmt.Errorf("ffmpeg hls playlist stalled for %s without a new segment (media_sequence=%d latest_segment=%q)",
					stallTimeout, progress.mediaSequence, progress.latestSegment)
			}
		}
	}
}

func readPlaylistProgress(indexPath string) (playlistProgress, bool) {
	body, err := os.ReadFile(indexPath)
	if err != nil {
		return playlistProgress{}, false
	}
	var progress playlistProgress
	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(rawLine)
		if value, ok := strings.CutPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"); ok {
			sequence, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err == nil {
				progress.mediaSequence = sequence
				progress.hasSequence = true
			}
			continue
		}
		if line != "" && !strings.HasPrefix(line, "#") {
			progress.latestSegment = line
		}
	}
	return progress, progress.latestSegment != ""
}

func playlistWatchInterval(hlsTime int) time.Duration {
	seconds := min(max(hlsTime, 1), 4)
	interval := time.Duration(seconds) * time.Second / 2
	return min(max(interval, 500*time.Millisecond), 2*time.Second)
}

func playlistStallTimeout(hlsTime, hlsListSize int) time.Duration {
	segmentSeconds := min(max(hlsTime, 1), 120)
	windowSegments := min(max(hlsListSize, 1), 120)
	segmentDuration := time.Duration(segmentSeconds) * time.Second
	timeout := segmentDuration * time.Duration(windowSegments+4)
	return min(max(timeout, 15*time.Second), 2*time.Minute)
}

func (j *DashJob) fail(err error) {
	if err == nil {
		return
	}
	j.mu.Lock()
	if j.intentional || j.err != nil {
		j.mu.Unlock()
		return
	}
	j.err = err
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func resolveMPD(ctx context.Context, opt DashOptions) (string, string, error) {
	return fetchPinnedMPD(ctx, opt)
}

func refreshFFmpegMPD(
	ctx context.Context,
	opt DashOptions,
	path string,
	headerOrigin string,
	headers map[string]string,
	interval time.Duration,
	log *slog.Logger,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		resolvedURL, body, err := fetchPinnedMPD(ctx, opt)
		if err == nil && hasFFmpegCustomHeaders(headers) && !sameURLOrigin(resolvedURL, headerOrigin) {
			err = fmt.Errorf("dash manifest refresh crossed the authorized header origin")
		}
		if err == nil {
			body, _, err = FilterMPDForPackWithSelection(
				body,
				resolvedURL,
				opt.PreferHeight,
				opt.VideoRepresentationID,
				opt.AudioRepresentationID,
			)
		}
		if err == nil {
			err = validateFFmpegMPD(body, resolvedURL, opt.Pull, headers)
		}
		if err == nil {
			err = writeFFmpegMPD(path, []byte(body))
		}
		if err != nil && ctx.Err() == nil {
			log.Warn("dash manifest refresh failed", "err", redactLogError(err))
		}
	}
}

func readLocalMPD(rawURL string) (string, string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}
	path := rawURL
	if strings.EqualFold(u.Scheme, "file") {
		path = u.Path
	} else if u.Scheme != "" || u.Host != "" {
		return "", "", fmt.Errorf("invalid local mpd path")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return "", "", err
	}
	if len(body) > 8<<20 {
		return "", "", fmt.Errorf("local mpd is too large")
	}
	if !strings.Contains(string(body), "<MPD") && !strings.Contains(string(body), "<mpd") {
		return "", "", fmt.Errorf("local source did not return MPD")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", err
	}
	return (&url.URL{Scheme: "file", Path: absPath}).String(), string(body), nil
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
