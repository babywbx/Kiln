package httpserver_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
)

func TestHLSPlayEndToEnd(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:1.0,\nseg0.ts\n")
		case "/live/seg0.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write([]byte("FAKE-TS"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(origin.Close)

	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := config.File{
		Server: config.Server{
			Listen:        "127.0.0.1:0",
			PublicBaseURL: "http://kiln.test",
			DataDir:       dir,
			ReadTimeout:   5,
			IdleTimeout:   30,
		},
		Auth: config.Auth{
			TokenTTLHours:   1,
			LoginRatePerMin: 100,
			TokenIssuer:     "kiln",
			TokenAudience:   "kiln",
			Users: []config.User{{
				Username:     "admin",
				PasswordHash: hash,
				Role:         "admin",
			}},
		},
		Security: config.Security{
			PlayRequireAuth:  true,
			MaxPlaylistBytes: 1 << 20,
			MaxBodyBytes:     1 << 20,
		},
		Upstreams: []config.Upstream{{
			ID:      "origin",
			BaseURL: origin.URL,
		}},
		Channels: []config.Channel{{
			ID:             "demo",
			Title:          "Demo",
			Upstream:       "origin",
			Path:           "/live/index.m3u8",
			Ingress:        "hls",
			OnDemand:       true,
			IdleTimeoutSec: 30,
			UserAgent:      "kiln-test",
		}},
		FFmpeg: config.FFmpeg{Binary: "ffmpeg", HLSTime: 2, HLSListSize: 4},
	}
	cfg.Security.PlayRequireAuth = true
	allowed := cfg.AllowedHostSet()

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
	puller := pull.New(pull.Options{Observe: obs, Allowed: allowed, MaxPlaylist: cfg.Security.MaxPlaylistBytes})
	sessions := session.NewManager(cat, puller, obs, dir, cfg.FFmpeg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	sessions.Start(t.Context())

	srv := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Auth:     authSvc,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Store:    db,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Allowed:  allowed,
	})

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	loginBody := []byte(`{"username":"admin","password":"secret"}`)
	resp, err := http.Post(ts.URL+"/v1/auth/login", "application/json", bytes.NewReader(loginBody))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("login %d %s", resp.StatusCode, b)
	}
	var login auth.LoginResult
	if err := json.NewDecoder(resp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/play/demo/index.m3u8?token="+login.Token, nil)
	presp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer presp.Body.Close()
	pb, _ := io.ReadAll(presp.Body)
	if presp.StatusCode != 200 {
		t.Fatalf("playlist %d %s", presp.StatusCode, pb)
	}
	if !strings.Contains(string(pb), "/v1/play/demo/u/") {
		t.Fatalf("rewrite missing: %s", pb)
	}

	var segURL string
	for _, line := range strings.Split(string(pb), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/v1/play/") {
			segURL = ts.URL + line
			break
		}
	}
	if segURL == "" {
		t.Fatalf("no segment url in %s", pb)
	}
	sresp, err := http.Get(segURL)
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	sb, _ := io.ReadAll(sresp.Body)
	if sresp.StatusCode != 200 || string(sb) != "FAKE-TS" {
		t.Fatalf("segment %d %q", sresp.StatusCode, sb)
	}

	preq, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/playlist.m3u", nil)
	preq.Header.Set("Authorization", "Bearer "+login.Token)
	pl, err := http.DefaultClient.Do(preq)
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Body.Close()
	plb, _ := io.ReadAll(pl.Body)
	if pl.StatusCode != 200 || !strings.Contains(string(plb), "demo") {
		t.Fatalf("playlist.m3u %d %s", pl.StatusCode, plb)
	}

	_ = os.RemoveAll(filepath.Join(dir, "sessions"))
}
