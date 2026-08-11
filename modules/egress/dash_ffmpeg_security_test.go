package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/pull"
)

func TestResolveMPDUsesDefaultDestinationValidation(t *testing.T) {
	var reached atomic.Bool
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = io.WriteString(w, "<MPD></MPD>")
	}))
	defer origin.Close()

	privateURL := strings.Replace(origin.URL, "127.0.0.1", "localhost", 1) + "/manifest.mpd"
	_, _, err := resolveMPD(context.Background(), DashOptions{
		SourceURL: privateURL,
		Pull:      pull.New(pull.Options{}),
	})
	if err == nil {
		t.Fatal("FFmpeg MPD probe reached a private DNS destination")
	}
	if reached.Load() {
		t.Fatal("private MPD origin received a request")
	}
}

func TestResolveMPDStripsHeadersFromCrossOriginRedirect(t *testing.T) {
	targetHeader := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHeader <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "<MPD></MPD>")
	}))
	defer target.Close()

	originHeader := make(chan string, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHeader <- r.Header.Get("X-Channel-Secret")
		http.Redirect(w, r, target.URL+"/manifest.mpd", http.StatusFound)
	}))
	defer origin.Close()

	_, _, err := resolveMPD(context.Background(), DashOptions{
		SourceURL: origin.URL + "/entry",
		Headers:   map[string]string{"X-Channel-Secret": "top-secret"},
		Pull:      pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-originHeader; got != "top-secret" {
		t.Fatalf("source header = %q, want channel secret", got)
	}
	if got := <-targetHeader; got != "" {
		t.Fatalf("redirect target header = %q, want empty", got)
	}
}

func TestFFmpegProxyPinsHTTPAndStripsCrossOriginHeaders(t *testing.T) {
	sourceSecret := make(chan string, 1)
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceSecret <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "source")
	}))
	defer source.Close()
	targetSecret := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetSecret <- r.Header.Get("X-Channel-Secret")
		_, _ = io.WriteString(w, "target")
	}))
	defer target.Close()

	proxy, err := startFFmpegForwardProxy(ffmpegProxyOptions{
		Client:       pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
		HeaderOrigin: source.URL,
		Headers:      map[string]string{"X-Channel-Secret": "top-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	for _, targetURL := range []string{source.URL, target.URL} {
		response, err := client.Get(targetURL)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	if got := <-sourceSecret; got != "top-secret" {
		t.Fatalf("source secret = %q, want configured header", got)
	}
	if got := <-targetSecret; got != "" {
		t.Fatalf("cross-origin secret = %q, want empty", got)
	}
}

func TestFFmpegProxyFollowsRedirectsForInsecureUpgrade(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/entry" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		_, _ = io.WriteString(w, "final")
	}))
	defer origin.Close()
	proxy, err := startFFmpegForwardProxy(ffmpegProxyOptions{
		Client:                   pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
		HeaderOrigin:             origin.URL,
		UpgradeInsecureRedirects: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get(origin.URL + "/entry")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "final" {
		t.Fatalf("redirect response = %d %q, err=%v", response.StatusCode, body, err)
	}
}

func TestFFprobeRoutesNestedDASHRequestsThroughGuardedProxy(t *testing.T) {
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}
	demuxers, err := exec.Command(ffprobe, "-v", "quiet", "-demuxers").Output()
	if err != nil || !strings.Contains(string(demuxers), " D  dash ") {
		t.Skip("ffprobe does not include the DASH demuxer")
	}
	var authorized atomic.Int64
	var unauthorized atomic.Int64
	files := http.FileServer(http.Dir(filepath.Join("..", "..", "testdata", "cenc", "h264")))
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Channel-Secret") != "top-secret" {
			unauthorized.Add(1)
			http.Error(w, "missing source credentials", http.StatusForbidden)
			return
		}
		authorized.Add(1)
		files.ServeHTTP(w, r)
	}))
	defer origin.Close()

	source, err := os.ReadFile(filepath.Join("..", "..", "testdata", "cenc", "h264", "stream.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	clearManifest := strings.ReplaceAll(string(source),
		`<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/>`, "")
	filtered, _, err := FilterMPDForPack(clearManifest, origin.URL+"/stream.mpd", 0)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "input.mpd")
	if err := os.WriteFile(input, []byte(filtered), 0o600); err != nil {
		t.Fatal(err)
	}
	guardedClient := pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	proxy, err := startFFmpegForwardProxy(ffmpegProxyOptions{
		Client:       guardedClient,
		HeaderOrigin: origin.URL,
		Headers:      map[string]string{"X-Channel-Secret": "top-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-protocol_whitelist", "file,http,https,tcp,tls,crypto,httpproxy",
		"-http_proxy", proxy.URL(),
		"-show_entries", "format=duration",
		input,
	)
	command.Env = append(os.Environ(), proxy.Env()...)
	output, runErr := command.CombinedOutput()
	if authorized.Load() == 0 {
		t.Fatalf("ffprobe did not route nested DASH requests through the guarded proxy: %v: %s", runErr, output)
	}
	if unauthorized.Load() != 0 {
		t.Fatalf("ffprobe bypassed the guarded proxy %d times", unauthorized.Load())
	}
}

func TestFFmpegProxyPinsHTTPSTunnel(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer target.Close()
	proxy, err := startFFmpegForwardProxy(ffmpegProxyOptions{
		Client: pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := target.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport}
	response, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "ok" {
		t.Fatalf("HTTPS proxy body = %q, err = %v", body, err)
	}
}

func TestFFmpegProxyBlocksCrossOriginHTTPSTunnelWithSecrets(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Store(true)
		_, _ = io.WriteString(w, "unexpected")
	}))
	defer target.Close()
	proxy, err := startFFmpegForwardProxy(ffmpegProxyOptions{
		Client:       pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
		HeaderOrigin: "https://origin.example/live.mpd",
		Headers:      map[string]string{"Authorization": "Bearer secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	transport := target.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	client := &http.Client{Transport: transport}
	if _, err := client.Get(target.URL); err == nil {
		t.Fatal("cross-origin HTTPS tunnel was accepted with source credentials")
	}
	if reached.Load() {
		t.Fatal("cross-origin HTTPS target received a request")
	}
}

func TestFFmpegProxyRequiresAuthentication(t *testing.T) {
	proxy, err := startFFmpegForwardProxy(ffmpegProxyOptions{Client: pull.New(pull.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL())
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = nil
	response, err := http.Get(proxyURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated status = %d, want 407", response.StatusCode)
	}
}

func TestValidateFFmpegMPDRejectsProtocolAndTemplateEscapes(t *testing.T) {
	client := pull.New(pull.Options{})
	for _, body := range []string{
		`<!DOCTYPE MPD><MPD></MPD>`,
		`<MPD><BaseURL>tcp://169.254.169.254:80</BaseURL></MPD>`,
		`<MPD><Representation id="//169.254.169.254/x"/></MPD>`,
	} {
		if err := validateFFmpegMPD(body, "https://media.example/live.mpd", client, nil); err == nil {
			t.Fatalf("unsafe MPD passed validation: %s", body)
		}
	}
	if err := validateFFmpegMPD(
		`<MPD><BaseURL>https://cdn.example/live/</BaseURL><Period id="urn:example:period:1"><SupplementalProperty schemeIdUri="urn:mpeg:dash:test" value="urn:example:value"/><Representation id="video-1"/></Period></MPD>`,
		"https://media.example/live.mpd",
		client,
		nil,
	); err != nil {
		t.Fatalf("public CDN required an explicit allowlist: %v", err)
	}
}

func TestRefreshFFmpegMPDReplacesDynamicSnapshot(t *testing.T) {
	var generation atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		current := generation.Load()
		_, _ = io.WriteString(w, fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.1S"><!-- generation-%d --><Period><AdaptationSet><Representation id="v%d" bandwidth="1" width="1" height="1"><SegmentTemplate initialization="init.mp4" media="seg-$Number$.m4s"/></Representation></AdaptationSet></Period></MPD>`, current, current))
	}))
	defer origin.Close()

	path := filepath.Join(t.TempDir(), "input.mpd")
	if err := os.WriteFile(path, []byte("initial"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options := DashOptions{
		SourceURL:                origin.URL + "/live.mpd",
		Pull:                     pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
		UpgradeInsecureRedirects: true,
	}
	proxy := &ffmpegForwardProxy{}
	proxy.upgradeHTTP.Store(true)
	generation.Store(2)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go refreshFFmpegMPD(ctx, options, path, origin.URL, nil, proxy, 20*time.Millisecond, logger)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(body), "generation-2") {
			if proxy.upgradeHTTP.Load() {
				t.Fatal("explicit HTTP refresh left proxy upgrades enabled")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("dynamic MPD snapshot was not refreshed")
}

func TestRefreshFFmpegMPDKeepsSnapshotAfterHeaderOriginChange(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `<MPD type="dynamic"><Period/></MPD>`)
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, destination.URL+"/live.mpd", http.StatusFound)
	}))
	defer origin.Close()

	path := filepath.Join(t.TempDir(), "input.mpd")
	const initial = `<MPD type="dynamic"><!-- usable --></MPD>`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	options := DashOptions{
		SourceURL: origin.URL + "/live.mpd",
		Headers:   map[string]string{"Authorization": "Bearer secret"},
		Pull:      pull.New(pull.Options{Allowed: map[string]struct{}{"127.0.0.1": {}}}),
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go refreshFFmpegMPD(ctx, options, path, origin.URL, options.Headers, nil, 10*time.Millisecond, logger)
	time.Sleep(100 * time.Millisecond)
	cancel()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != initial {
		t.Fatalf("cross-origin refresh replaced usable snapshot: %s", body)
	}
}

func TestRedactLogErrorRemovesURLSecrets(t *testing.T) {
	err := &url.Error{Op: "Get", URL: "https://user:password@media.example/session/secret/live.mpd?token=secret", Err: errors.New("dial failed")}
	redacted := redactLogError(err)
	if strings.Contains(redacted, "secret") || strings.Contains(redacted, "password") ||
		!strings.Contains(redacted, "https://media.example") {
		t.Fatalf("unexpected redacted error: %q", redacted)
	}
}

func TestFFmpegMPDRefreshInterval(t *testing.T) {
	if got := ffmpegMPDRefreshInterval(`<MPD type="dynamic" minimumUpdatePeriod="PT0.1S"/>`); got != 500*time.Millisecond {
		t.Fatalf("dynamic refresh interval = %v, want 500ms floor", got)
	}
	if got := ffmpegMPDRefreshInterval(`<MPD type="static"/>`); got != 0 {
		t.Fatalf("static refresh interval = %v, want zero", got)
	}
}
