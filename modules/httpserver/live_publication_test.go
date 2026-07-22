package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
)

const (
	fakeMaster = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="audio-main",URI="audio-main.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=152000,CODECS="hvc1.1.6.L60.90,mp4a.40.2",AUDIO="audio"
video-main.m3u8
`
	fakeVideo = `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:2
#EXT-X-MAP:URI="video-main-init.mp4"
#EXTINF:2.000000,
video-main-000001.m4s
`
)

// fakePublication stands in for a real engine so the HTTP layer can be tested
// without launching ffmpeg or reaching upstream.
type fakePublication struct {
	dir                   string
	assets                map[string]string
	playlistContextResult func(context.Context) error
	assetContextResult    func(context.Context) error
}

func (p *fakePublication) PlaylistContext(ctx context.Context, name string, _ packager.PlaylistRequest) (packager.PlaylistView, bool, error) {
	if p.playlistContextResult != nil {
		if err := p.playlistContextResult(ctx); err != nil {
			return packager.PlaylistView{}, true, err
		}
	}
	body, ok := p.Playlist(name)
	return packager.PlaylistView{Body: body}, ok, nil
}

func (p *fakePublication) Master() string { return "master.m3u8" }

func (p *fakePublication) Playlist(name string) ([]byte, bool) {
	switch name {
	case "master.m3u8":
		return []byte(fakeMaster), true
	case "video-main.m3u8":
		return []byte(fakeVideo), true
	case "audio-main.m3u8":
		return []byte("#EXTM3U\n#EXT-X-MAP:URI=\"audio-main-init.mp4\"\n"), true
	}
	return nil, false
}

func (p *fakePublication) Asset(name string) (packager.Asset, bool) {
	path, ok := p.assets[name]
	if !ok {
		return packager.Asset{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return packager.Asset{}, false
	}
	return packager.Asset{Path: path, Immutable: true, ModTime: st.ModTime()}, true
}

func (p *fakePublication) AssetContext(ctx context.Context, name string) (packager.Asset, bool, error) {
	if p.assetContextResult != nil {
		if err := p.assetContextResult(ctx); err != nil {
			return packager.Asset{}, true, err
		}
	}
	asset, ok := p.Asset(name)
	return asset, ok, nil
}

type fakeJob struct {
	pub  *fakePublication
	done chan struct{}
}

func (j *fakeJob) Publication() packager.Publication { return j.pub }
func (j *fakeJob) Engine() string                    { return packager.EngineNativeRewrite }
func (j *fakeJob) PackMode() string                  { return "static_list" }
func (j *fakeJob) FallbackReason() string            { return "" }
func (j *fakeJob) Done() <-chan struct{}             { return j.done }
func (j *fakeJob) Err() error                        { return nil }
func (j *fakeJob) IntentionalStop() bool             { return true }
func (j *fakeJob) Stats() packager.Stats             { return packager.Stats{} }
func (j *fakeJob) Stop() error                       { close(j.done); return nil }

type fakePackager struct {
	configure func(*fakePublication)
}

func (p fakePackager) Start(_ context.Context, req packager.Request) (packager.Job, error) {
	if err := os.MkdirAll(req.WorkDir, 0o750); err != nil {
		return nil, err
	}
	assets := map[string]string{}
	for _, name := range []string{"video-main-init.mp4", "video-main-000001.m4s", "audio-main-init.mp4"} {
		path := filepath.Join(req.WorkDir, name)
		if err := os.WriteFile(path, []byte("payload-"+name), 0o600); err != nil {
			return nil, err
		}
		assets[name] = path
	}
	publication := &fakePublication{dir: req.WorkDir, assets: assets}
	if p.configure != nil {
		p.configure(publication)
	}
	return &fakeJob{
		pub:  publication,
		done: make(chan struct{}),
	}, nil
}

func newLiveServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	dir := t.TempDir()

	hash, err := auth.HashPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.File{
		Auth: config.Auth{
			TokenIssuer:   "kiln",
			TokenAudience: "kiln",
			Users:         []config.User{{Username: "admin", PasswordHash: hash, Role: "admin"}},
		},
		Security: config.Security{MaxPlaylistBytes: 1 << 20, MaxBodyBytes: 1 << 20},
		Upstreams: []config.Upstream{{
			ID:      "origin",
			BaseURL: "http://origin.invalid",
		}},
		Channels: []config.Channel{{
			ID:             "dash1",
			Title:          "Dash",
			Upstream:       "origin",
			Path:           "/stream.mpd",
			Ingress:        "dash",
			OnDemand:       true,
			IdleTimeoutSec: 30,
		}},
		Packager: config.Packager{Engine: config.EngineAuto, PlaylistSize: 8, GraceSec: 30},
	}

	obs := observe.New()
	authSvc, err := auth.New(cfg.Auth, time.Hour, auth.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	cat := catalog.New(cfg, db)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	puller := pull.New(pull.Options{Observe: obs, MaxPlaylist: cfg.Security.MaxPlaylistBytes})
	sessions := session.NewManager(cat, puller, obs, dir, cfg.FFmpeg, httpTestKeys(), log, nil)
	sessions.SetPackager(fakePackager{})

	srv := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Auth:     authSvc,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Store:    db,
		Log:      log,
		Allowed:  cfg.AllowedHostSet(),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, sessions
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A player must be able to walk master -> media playlist -> init + segment
// entirely through this server, with no reference escaping the publication.
func TestLivePublicationServesFullChain(t *testing.T) {
	ts, _ := newLiveServer(t)

	master := body(t, get(t, ts.URL+"/v1/play/dash1/index.m3u8"))
	for _, want := range []string{
		`URI="/v1/play/dash1/live/audio-main.m3u8?g=`,
		"/v1/play/dash1/live/video-main.m3u8?g=",
	} {
		if !strings.Contains(master, want) {
			t.Fatalf("master playlist is missing %q:\n%s", want, master)
		}
	}

	video := body(t, get(t, ts.URL+"/v1/play/dash1/live/video-main.m3u8"))
	for _, want := range []string{
		`#EXT-X-MAP:URI="/v1/play/dash1/live/video-main-init.mp4?g=`,
		"/v1/play/dash1/live/video-main-000001.m4s?g=",
	} {
		if !strings.Contains(video, want) {
			t.Fatalf("video playlist is missing %q:\n%s", want, video)
		}
	}

	seg := get(t, ts.URL+"/v1/play/dash1/live/video-main-000001.m4s")
	if seg.StatusCode != http.StatusOK {
		t.Fatalf("segment status = %d", seg.StatusCode)
	}
	if got := seg.Header.Get("Content-Type"); got != "video/mp4" {
		t.Errorf("segment content type = %s, want video/mp4", got)
	}
	if got := body(t, seg); got != "payload-video-main-000001.m4s" {
		t.Errorf("segment body = %q", got)
	}
}

// Published segments are immutable, so they must escape the global no-store.
// Without this the shared upstream fetch buys nothing: every player re-reads
// every byte from us.
func TestImmutableAssetsAreCacheable(t *testing.T) {
	ts, _ := newLiveServer(t)
	// Segments no longer start a session, so the playlist has to come first.
	pl0 := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	pl0.Body.Close()

	seg := get(t, ts.URL+"/v1/play/dash1/live/video-main-000001.m4s")
	defer seg.Body.Close()
	if got := seg.Request.URL.Query().Get("g"); got == "" {
		t.Fatal("immutable segment was not redirected onto a publication generation")
	}
	cc := seg.Header.Get("Cache-Control")
	if !strings.Contains(cc, "immutable") || !strings.Contains(cc, "max-age=") {
		t.Errorf("segment Cache-Control = %q, want an immutable max-age", cc)
	}
	// The URL can carry an access token, so a shared cache must not hold it.
	if strings.Contains(cc, "public") {
		t.Errorf("segment Cache-Control = %q, must not be public", cc)
	}
	if seg.Header.Get("Pragma") != "" {
		t.Errorf("segment still carries the global Pragma: no-cache header")
	}

	pl := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	defer pl.Body.Close()
	if got := pl.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("playlist Cache-Control = %q, want no-store", got)
	}
}

func TestPublishedFMP4AssetsSupportSingleAndMultipleRanges(t *testing.T) {
	ts, _ := newLiveServer(t)
	playlist := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	playlist.Body.Close()
	assetURL := ts.URL + "/v1/play/dash1/live/video-main-000001.m4s"

	request, err := http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-6")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("Content-Range") != "bytes 0-6/29" {
		t.Fatalf("single range status=%d content-range=%q", response.StatusCode, response.Header.Get("Content-Range"))
	}
	if got := body(t, response); got != "payload" {
		t.Fatalf("single range body = %q", got)
	}

	request, err = http.NewRequest(http.MethodGet, assetURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-2,8-10")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/byteranges" {
		t.Fatalf("multi range content type = %q, err=%v", response.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(response.Body, params["boundary"])
	var parts []string
	for {
		part, partErr := reader.NextPart()
		if partErr == io.EOF {
			break
		}
		if partErr != nil {
			t.Fatal(partErr)
		}
		data, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Fatal(readErr)
		}
		parts = append(parts, string(data))
	}
	if len(parts) != 2 || parts[0] != "pay" || parts[1] != "vid" {
		t.Fatalf("multi range parts = %#v", parts)
	}
}

// The cache exemption is per route. Widening the global default instead would
// have quietly relaxed admin and status responses too.
func TestAdminResponsesStayUncacheable(t *testing.T) {
	ts, _ := newLiveServer(t)

	resp := get(t, ts.URL+"/v1/status")
	defer resp.Body.Close()
	if got := resp.Header.Get("Cache-Control"); !strings.Contains(got, "no-store") {
		t.Errorf("status Cache-Control = %q, want no-store", got)
	}
}

// A late segment request must not resurrect an idle-stopped channel. The player
// is told the session is gone and goes back to the playlist, which is what
// legitimately restarts it.
func TestSegmentRequestDoesNotStartSession(t *testing.T) {
	ts, sessions := newLiveServer(t)

	resp := get(t, ts.URL+"/v1/play/dash1/live/video-main-000001.m4s")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("segment status = %d, want 410 for a channel that is not running", resp.StatusCode)
	}
	if _, ok := sessions.Get("dash1"); ok {
		t.Fatal("a segment request started a session")
	}

	// The playlist is what acquires.
	pl := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	defer pl.Body.Close()
	if pl.StatusCode != http.StatusOK {
		t.Fatalf("playlist status = %d", pl.StatusCode)
	}
	if _, ok := sessions.Get("dash1"); !ok {
		t.Fatal("the playlist request did not start a session")
	}
}

func TestOldPublicationGenerationReanchorsPlaylistsAndRejectsSegments(t *testing.T) {
	ts, sessions := newLiveServer(t)
	masterBody := body(t, get(t, ts.URL+"/v1/play/dash1/index.m3u8"))
	var oldPlaylistURL string
	for _, line := range strings.Split(masterBody, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/v1/play/dash1/live/video-main.m3u8?") {
			oldPlaylistURL = ts.URL + line
			break
		}
	}
	if oldPlaylistURL == "" {
		t.Fatalf("master has no generated media URL:\n%s", masterBody)
	}
	parsed, err := url.Parse(oldPlaylistURL)
	if err != nil || parsed.Query().Get("g") == "" {
		t.Fatalf("media URL has no generation: %q", oldPlaylistURL)
	}
	oldGeneration := parsed.Query().Get("g")
	oldPlaylistBody := body(t, get(t, oldPlaylistURL))
	var oldSegmentURL string
	for _, line := range strings.Split(oldPlaylistBody, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, ".m4s?") && !strings.HasPrefix(line, "#") {
			oldSegmentURL = ts.URL + line
			break
		}
	}
	if oldSegmentURL == "" {
		t.Fatalf("media playlist has no generated segment URL:\n%s", oldPlaylistBody)
	}

	if !sessions.StopChannel("dash1") {
		t.Fatal("session was not stopped")
	}
	restarted := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	_ = restarted.Body.Close()

	noRedirect := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	oldPlaylist, err := noRedirect.Get(oldPlaylistURL)
	if err != nil {
		t.Fatal(err)
	}
	defer oldPlaylist.Body.Close()
	if oldPlaylist.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("old playlist generation status = %d", oldPlaylist.StatusCode)
	}
	redirected, err := url.Parse(oldPlaylist.Header.Get("Location"))
	if err != nil || redirected.Query().Get("g") == "" || redirected.Query().Get("g") == oldGeneration {
		t.Fatalf("old playlist redirect = %q", oldPlaylist.Header.Get("Location"))
	}
	currentPlaylist := get(t, ts.URL+redirected.String())
	if currentPlaylist.StatusCode != http.StatusOK {
		t.Fatalf("redirected playlist status = %d", currentPlaylist.StatusCode)
	}
	_ = currentPlaylist.Body.Close()

	oldSegment := get(t, oldSegmentURL)
	defer oldSegment.Body.Close()
	if oldSegment.StatusCode != http.StatusGone {
		t.Fatalf("old segment generation status = %d", oldSegment.StatusCode)
	}
	if got := oldSegment.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q", got)
	}
}

// Only registered assets are reachable. The work directory is not a document
// root, whatever path a request asks for.
func TestUnpublishedAssetIsNotServed(t *testing.T) {
	ts, _ := newLiveServer(t)
	// Start the session first, so a 404 here means the whitelist rejected it.
	pl := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	pl.Body.Close()

	for _, name := range []string{"input.mpd", "ffmpeg.stderr.log", "video-main-000099.m4s"} {
		resp := get(t, ts.URL+"/v1/play/dash1/live/"+name)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s should not be served", name)
		}
	}
}

func TestLLHLSWaitsHaveHTTPDeadlines(t *testing.T) {
	ts, sessions := newLiveServer(t)
	deadlineSeen := func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("LL-HLS wait has no deadline")
		} else if remaining := time.Until(deadline); remaining <= 0 || remaining > 16*time.Second {
			t.Errorf("LL-HLS deadline is %s away", remaining)
		}
		return context.DeadlineExceeded
	}
	sessions.SetPackager(fakePackager{configure: func(publication *fakePublication) {
		publication.playlistContextResult = deadlineSeen
		publication.assetContextResult = deadlineSeen
	}})

	master := get(t, ts.URL+"/v1/play/dash1/index.m3u8")
	_ = master.Body.Close()
	playlist := get(t, ts.URL+"/v1/play/dash1/live/video-main.m3u8?_HLS_msn=2")
	playlistBody := body(t, playlist)
	if playlist.StatusCode != http.StatusOK || !strings.Contains(playlistBody, "video-main-000001.m4s") {
		t.Fatalf("timed out playlist = %d %s", playlist.StatusCode, playlistBody)
	}

	part := get(t, ts.URL+"/v1/play/dash1/live/video-part-000002-000.m4s")
	defer part.Body.Close()
	if part.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("timed out part status = %d", part.StatusCode)
	}
	if got := part.Header.Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q", got)
	}
}
