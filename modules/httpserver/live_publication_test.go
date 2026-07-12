package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
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
	dir    string
	assets map[string]string
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
func (j *fakeJob) Stop() error                       { close(j.done); return nil }

type fakePackager struct{}

func (fakePackager) Start(_ context.Context, req packager.Request) (packager.Job, error) {
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
	return &fakeJob{
		pub:  &fakePublication{dir: req.WorkDir, assets: assets},
		done: make(chan struct{}),
	}, nil
}

func newLiveServer(t *testing.T) (*httptest.Server, *session.Manager) {
	t.Helper()
	dir := t.TempDir()

	keysFile := filepath.Join(dir, "channel.keys")
	if err := os.WriteFile(keysFile, []byte("ffeeddccbbaa99887766554433221100:00112233445566778899aabbccddeeff\n"), 0o600); err != nil {
		t.Fatal(err)
	}

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
			KeysFile:       keysFile,
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
	sessions := session.NewManager(cat, puller, obs, dir, cfg.FFmpeg, log, nil)
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
		`URI="/v1/play/dash1/live/audio-main.m3u8"`,
		"/v1/play/dash1/live/video-main.m3u8",
	} {
		if !strings.Contains(master, want) {
			t.Fatalf("master playlist is missing %q:\n%s", want, master)
		}
	}

	video := body(t, get(t, ts.URL+"/v1/play/dash1/live/video-main.m3u8"))
	for _, want := range []string{
		`#EXT-X-MAP:URI="/v1/play/dash1/live/video-main-init.mp4"`,
		"/v1/play/dash1/live/video-main-000001.m4s",
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
