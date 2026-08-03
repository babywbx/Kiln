package liteserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/liteserver"
)

func TestHealthAndReadinessAreAvailableWithoutAdmin(t *testing.T) {
	server, err := liteserver.New(withTestAuth(t, config.File{}), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path string
		code int
		body string
	}{
		{path: "/healthz", code: http.StatusOK, body: `"status":"ok"`},
		{path: "/readyz", code: http.StatusOK, body: `"status":"ready"`},
		{path: "/admin", code: http.StatusNotFound, body: `404 page not found`},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.code {
				t.Fatalf("status = %d, want %d", response.Code, test.code)
			}
			if !strings.Contains(response.Body.String(), test.body) {
				t.Fatalf("body = %q, want substring %q", response.Body.String(), test.body)
			}
		})
	}
}

func TestPublicHostAllowlistIsEnforced(t *testing.T) {
	server, err := liteserver.New(withTestAuth(t, config.File{
		Security: config.Security{PublicHosts: []string{"kiln.example"}},
	}), testLogger())
	if err != nil {
		t.Fatal(err)
	}

	allowed := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	allowed.Host = "kiln.example:8080"
	allowedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(allowedResponse, allowed)
	if allowedResponse.Code != http.StatusOK {
		t.Fatalf("allowed host status = %d", allowedResponse.Code)
	}

	blocked := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	blocked.Host = "unexpected.example"
	blockedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(blockedResponse, blocked)
	if blockedResponse.Code != http.StatusForbidden {
		t.Fatalf("blocked host status = %d", blockedResponse.Code)
	}

	localHealth := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	localHealth.Host = "127.0.0.1"
	localHealth.RemoteAddr = "127.0.0.1:43210"
	localResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(localResponse, localHealth)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local health status = %d", localResponse.Code)
	}
}

func TestHLSPlaybackRewritesAndProxiesMedia(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:2,\nsegment.ts\n")
		case "/segment.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = io.WriteString(w, "media")
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()

	cfg := config.File{
		Server: config.Server{PublicBaseURL: "http://kiln.test"},
		Security: config.Security{
			PlayRequireAuth: config.Bool(false),
			AllowedHosts:    []string{"127.0.0.1"}, MaxPlaylistBytes: 1 << 20,
		},
		Packager: config.Packager{Engine: config.EngineNative},
		Channels: []config.Channel{{
			ID: "news", SourceURL: origin.URL + "/index.m3u8", Ingress: "hls", OnDemand: true,
		}},
	}
	server, err := liteserver.New(withTestAuth(t, cfg), testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	loginRequest := httptest.NewRequest(
		http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"admin","password":"admin"}`),
	)
	loginResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("open-play login status = %d, body = %s", loginResponse.Code, loginResponse.Body.String())
	}

	index := httptest.NewRecorder()
	server.Handler().ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/v1/play/news/index.m3u8", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status = %d, body = %s", index.Code, index.Body.String())
	}
	lines := strings.Split(strings.TrimSpace(index.Body.String()), "\n")
	mediaPath := lines[len(lines)-1]
	parsed, err := url.Parse(mediaPath)
	if err != nil || !strings.HasPrefix(parsed.Path, "/v1/play/news/u/") {
		t.Fatalf("rewritten media path = %q", mediaPath)
	}

	media := httptest.NewRecorder()
	server.Handler().ServeHTTP(media, httptest.NewRequest(http.MethodGet, mediaPath, nil))
	if media.Code != http.StatusOK || media.Body.String() != "media" {
		t.Fatalf("media status = %d, body = %q", media.Code, media.Body.String())
	}
}

func TestNativeDASHPlaybackPublishesFullChain(t *testing.T) {
	originRoot := filepath.Join("..", "..", "testdata", "cenc", "h264")
	origin := httptest.NewServer(http.FileServer(http.Dir(originRoot)))
	defer origin.Close()
	t.Setenv("KILN_PLAY_OPEN", "1")

	workingDirectory := t.TempDir()
	keysPath, err := filepath.Abs(filepath.Join("..", "..", "deploy", "docker", "core-smoke.keys"))
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(workingDirectory, "kiln.toml")
	configText := fmt.Sprintf(`
[server]
data_dir = %q

[auth]
[[auth.users]]
username = "admin"
password_hash = "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie"
role = "admin"

[security]
allowed_hosts = ["127.0.0.1"]

[packager]
engine = "native"
keys_file = %q
start_segments = 1
prefetch_segments = 1

[epg]
enabled = false

[[channels]]
id = "dash"
source_url = %q
ingress = "dash"
on_demand = true
`, workingDirectory, keysPath, origin.URL+"/stream.mpd")
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	server, err := liteserver.New(cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	master := request(t, server, "/v1/play/dash/index.m3u8")
	if master.Code != http.StatusOK || !strings.Contains(master.Body.String(), "/v1/play/dash/live/") {
		t.Fatalf("master status = %d, body = %s", master.Code, master.Body.String())
	}
	mediaPath := firstPlaylistPath(master.Body.String(), ".m3u8")
	media := request(t, server, mediaPath)
	if media.Code != http.StatusOK || !strings.Contains(media.Body.String(), ".m4s") {
		t.Fatalf("media status = %d, body = %s", media.Code, media.Body.String())
	}
	segmentPath := firstPlaylistPath(media.Body.String(), ".m4s")
	segment := request(t, server, segmentPath)
	if segment.Code != http.StatusOK || segment.Body.Len() == 0 {
		t.Fatalf("segment status = %d, bytes = %d", segment.Code, segment.Body.Len())
	}
}

func TestPlaybackAuthenticationUsesTheExistingLoginContract(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "#EXTM3U\n")
	}))
	defer origin.Close()
	passwordHash, err := auth.HashPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.File{
		Server: config.Server{DataDir: t.TempDir()},
		Auth: config.Auth{
			TokenIssuer: "kiln", TokenAudience: "kiln", TokenTTLHours: 1, LoginRatePerMin: 1,
			Users: []config.User{{
				Username: "viewer", PasswordHash: passwordHash, Role: "viewer", ChannelIDs: []string{"news"},
			}},
		},
		Security: config.Security{
			PlayRequireAuth: config.Bool(true), AllowedHosts: []string{"127.0.0.1"},
			MaxPlaylistBytes: 1 << 20, MaxBodyBytes: 1 << 20,
		},
		Packager: config.Packager{Engine: config.EngineNative},
		Channels: []config.Channel{{
			ID: "news", SourceURL: origin.URL, Ingress: "hls", OnDemand: true,
		}},
	}
	server, err := liteserver.New(cfg, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})

	unauthorized := request(t, server, "/v1/play/news/index.m3u8")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, body = %s", unauthorized.Code, unauthorized.Body.String())
	}
	loginRequest := httptest.NewRequest(
		http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"viewer","password":"secret-password"}`),
	)
	loginRequest.RemoteAddr = "192.0.2.10:10001"
	login := httptest.NewRecorder()
	server.Handler().ServeHTTP(login, loginRequest)
	if login.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", login.Code, login.Body.String())
	}
	if cacheControl := login.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("login Cache-Control = %q", cacheControl)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &result); err != nil || result.Token == "" {
		t.Fatalf("login response = %s, err = %v", login.Body.String(), err)
	}
	repeatedLogin := httptest.NewRequest(
		http.MethodPost, "/v1/auth/login", strings.NewReader(`{"username":"viewer","password":"secret-password"}`),
	)
	repeatedLogin.RemoteAddr = "192.0.2.10:10002"
	repeatedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(repeatedResponse, repeatedLogin)
	if repeatedResponse.Code != http.StatusTooManyRequests {
		t.Fatalf("repeated login status = %d, body = %s", repeatedResponse.Code, repeatedResponse.Body.String())
	}
	authorized := request(t, server, "/v1/play/news/index.m3u8?token="+url.QueryEscape(result.Token))
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", authorized.Code, authorized.Body.String())
	}
	playlist := request(t, server, "/v1/playlist.m3u?token="+url.QueryEscape(result.Token))
	if playlist.Code != http.StatusOK || !strings.Contains(playlist.Body.String(), "/v1/play/news/index.m3u8?token=") {
		t.Fatalf("playlist status = %d, body = %s", playlist.Code, playlist.Body.String())
	}
}

func request(t *testing.T, server *liteserver.Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	return response
}

func firstPlaylistPath(body, suffix string) string {
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") && strings.Contains(line, suffix) {
			return line
		}
	}
	return ""
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func withTestAuth(t *testing.T, cfg config.File) config.File {
	t.Helper()
	if cfg.Server.DataDir == "" {
		cfg.Server.DataDir = t.TempDir()
	}
	if len(cfg.Auth.Users) == 0 {
		cfg.Auth.Users = []config.User{{
			Username:     "admin",
			PasswordHash: "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie",
			Role:         "admin",
		}}
	}
	return cfg
}
