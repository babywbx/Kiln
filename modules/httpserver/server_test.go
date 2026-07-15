package httpserver_test

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/epg"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
)

func TestEPGEndToEnd(t *testing.T) {
	var fetches int
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches++
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<tv><channel id="368359"><display-name>無綫新聞台</display-name></channel>
<programme channel="368359" start="20260713090000 +0800" stop="20260713100000 +0800"><title>早晨新聞</title></programme></tv>`)
	}))
	t.Cleanup(origin.Close)

	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cache := false
	cfg := config.File{
		Server: config.Server{PublicBaseURL: "http://kiln.test", DataDir: dir, ReadTimeout: 5, IdleTimeout: 30},
		Auth: config.Auth{TokenIssuer: "kiln", TokenAudience: "kiln", Users: []config.User{{
			Username: "admin", PasswordHash: hash, Role: "admin",
		}}},
		Security: config.Security{MaxBodyBytes: 1 << 20},
		EPG: config.EPG{Enabled: false, Cache: &cache, Sources: []config.EPGSource{{
			ID: "fixture", Name: "Fixture", URL: origin.URL, Timezone: "Asia/Hong_Kong", Proxy: "direct", Enabled: true,
		}}},
		Channels: []config.Channel{{
			ID: "demo", Title: "無綫新聞台", EPGID: "368359", EPGName: "無綫新聞台", EPGSource: "fixture",
			Ingress: "hls", OnDemand: true,
		}},
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	authSvc, err := auth.New(cfg.Auth, time.Hour, auth.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	login, err := authSvc.Login("admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	source := epg.Source{ID: "fixture", Name: "Fixture", URL: origin.URL, Timezone: "Asia/Hong_Kong", Proxy: "direct"}
	epgSvc := epg.NewService(epg.ServiceConfig{Sources: []epg.Source{source}}, &epg.Fetcher{}, nil)
	srv := httpserver.New(httpserver.Deps{
		Cfg: cfg, Auth: authSvc, Catalog: catalog.New(cfg, db), Observe: observe.New(), Store: db, EPG: epgSvc,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	plain, err := http.Get(ts.URL + "/v1/epg.xml")
	if err != nil {
		t.Fatal(err)
	}
	plainBody, _ := io.ReadAll(plain.Body)
	_ = plain.Body.Close()
	if plain.StatusCode != http.StatusOK || !strings.Contains(string(plainBody), `channel id="demo"`) ||
		!strings.Contains(string(plainBody), `<programme`) || !strings.Contains(string(plainBody), `channel="demo"`) {
		t.Fatalf("plain EPG %d %s", plain.StatusCode, plainBody)
	}
	if fetches != 1 {
		t.Fatalf("cache=false should refresh before serving, fetches=%d", fetches)
	}

	compressed, err := http.Get(ts.URL + "/v1/epg.xml.gz")
	if err != nil {
		t.Fatal(err)
	}
	zipReader, err := gzip.NewReader(compressed.Body)
	if err != nil {
		t.Fatal(err)
	}
	compressedBody, _ := io.ReadAll(zipReader)
	_ = zipReader.Close()
	_ = compressed.Body.Close()
	if compressed.StatusCode != http.StatusOK || !strings.Contains(string(compressedBody), `channel id="demo"`) {
		t.Fatalf("gzip EPG %d %s", compressed.StatusCode, compressedBody)
	}
	if fetches != 2 {
		t.Fatalf("gzip read should also refresh with cache=false, fetches=%d", fetches)
	}

	playlist := adminJSON(t, http.MethodGet, ts.URL+"/v1/playlist.m3u", login.Token, nil)
	if playlist.StatusCode != http.StatusOK ||
		!strings.Contains(string(playlist.Body), `x-tvg-url="http://kiln.test/v1/epg.xml.gz"`) ||
		!strings.Contains(string(playlist.Body), `tvg-logo="http://kiln.test/v1/logo/demo"`) {
		t.Fatalf("EPG playlist %d %s", playlist.StatusCode, playlist.Body)
	}
	exported := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/exports/m3u", login.Token, map[string]any{})
	if exported.StatusCode != http.StatusCreated ||
		strings.Contains(string(exported.Body), login.Token) ||
		!strings.Contains(string(exported.Body), "/p/v1") ||
		!strings.Contains(string(exported.Body), "/play/demo/index.m3u8") {
		t.Fatalf("safe M3U export %d %s", exported.StatusCode, exported.Body)
	}
	distributionToken, distributionRow, err := accesstoken.NewRow("EPG", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertAccessToken(distributionRow); err != nil {
		t.Fatal(err)
	}
	distributionPlaylist, err := http.Get(ts.URL + "/p/" + distributionToken + "/playlist.m3u")
	if err != nil {
		t.Fatal(err)
	}
	distributionBody, _ := io.ReadAll(distributionPlaylist.Body)
	_ = distributionPlaylist.Body.Close()
	if distributionPlaylist.StatusCode != http.StatusOK ||
		!strings.Contains(string(distributionBody), `x-tvg-url="http://kiln.test/v1/epg.xml.gz"`) {
		t.Fatalf("distribution EPG playlist %d %s", distributionPlaylist.StatusCode, distributionBody)
	}

	presets := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/epg/presets", login.Token, nil)
	if presets.StatusCode != http.StatusOK || !strings.Contains(string(presets.Body), `"hk-1"`) {
		t.Fatalf("EPG presets %d %s", presets.StatusCode, presets.Body)
	}
	sources := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/epg/sources", login.Token, nil)
	if sources.StatusCode != http.StatusOK || !strings.Contains(string(sources.Body), `"fixture"`) {
		t.Fatalf("EPG sources %d %s", sources.StatusCode, sources.Body)
	}
	matches := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/epg/matches", login.Token, nil)
	if matches.StatusCode != http.StatusOK || !strings.Contains(string(matches.Body), `"status":"matched"`) {
		t.Fatalf("EPG matches %d %s", matches.StatusCode, matches.Body)
	}

	created := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/epg/sources", login.Token, map[string]any{
		"id": "backup", "name": "Backup", "url": origin.URL, "timezone": "Asia/Hong_Kong", "proxy": "direct", "enabled": false,
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create EPG source %d %s", created.StatusCode, created.Body)
	}
	var createdBody struct {
		Source epg.ConfiguredSource `json:"source"`
	}
	if err := json.Unmarshal(created.Body, &createdBody); err != nil || createdBody.Source.Revision == 0 {
		t.Fatalf("created EPG source = %+v, err=%v", createdBody, err)
	}
	update := map[string]any{
		"id": "backup", "name": "Backup 2", "url": origin.URL, "timezone": "Asia/Hong_Kong", "proxy": "auto", "enabled": true,
	}
	missingRevision := adminJSON(t, http.MethodPut, ts.URL+"/v1/admin/epg/sources/backup", login.Token, update)
	if missingRevision.StatusCode != http.StatusPreconditionRequired {
		t.Fatalf("missing EPG source revision %d %s", missingRevision.StatusCode, missingRevision.Body)
	}
	updated := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/epg/sources/backup", login.Token, update,
		map[string]string{"If-Match": strconv.FormatInt(createdBody.Source.Revision, 10)})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("update EPG source %d %s", updated.StatusCode, updated.Body)
	}
	stale := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/epg/sources/backup", login.Token, update,
		map[string]string{"If-Match": strconv.FormatInt(createdBody.Source.Revision, 10)})
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale EPG source update %d %s", stale.StatusCode, stale.Body)
	}
	var updatedBody struct {
		Source epg.ConfiguredSource `json:"source"`
	}
	if err := json.Unmarshal(updated.Body, &updatedBody); err != nil {
		t.Fatal(err)
	}
	deleted := adminJSONHeaders(t, http.MethodDelete, ts.URL+"/v1/admin/epg/sources/backup", login.Token, nil,
		map[string]string{"If-Match": strconv.FormatInt(updatedBody.Source.Revision, 10)})
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete EPG source %d %s", deleted.StatusCode, deleted.Body)
	}
	refreshed := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/epg/refresh", login.Token, nil)
	if refreshed.StatusCode != http.StatusOK || !strings.Contains(string(refreshed.Body), `"statuses"`) {
		t.Fatalf("refresh EPG %d %s", refreshed.StatusCode, refreshed.Body)
	}
}

func TestEPGRefreshRejectsNoActiveSources(t *testing.T) {
	hash, err := auth.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	authService, err := auth.New(config.Auth{
		TokenIssuer: "kiln", TokenAudience: "kiln",
		Users: []config.User{{Username: "admin", PasswordHash: hash, Role: "admin"}},
	}, time.Hour, auth.Options{DataDir: directory})
	if err != nil {
		t.Fatal(err)
	}
	login, err := authService.Login("admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	server := httpserver.New(httpserver.Deps{
		Cfg: config.File{
			Server:   config.Server{ReadTimeout: 5, IdleTimeout: 30},
			Security: config.Security{MaxBodyBytes: 1 << 20},
		},
		Auth: authService, Observe: observe.New(), EPG: epg.NewService(epg.ServiceConfig{}, nil, nil),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	testServer := httptest.NewServer(server.Handler())
	t.Cleanup(testServer.Close)

	response := adminJSON(t, http.MethodPost, testServer.URL+"/v1/admin/epg/refresh", login.Token, nil)
	if response.StatusCode != http.StatusConflict || !strings.Contains(string(response.Body), "enable at least one EPG source") {
		t.Fatalf("refresh without sources = %d %s", response.StatusCode, response.Body)
	}
}

func TestEPGUnavailableReturnsLegalEmptyDocument(t *testing.T) {
	cache := true
	server := httpserver.New(httpserver.Deps{
		Cfg: config.File{
			Server: config.Server{ReadTimeout: 5, IdleTimeout: 30},
			EPG:    config.EPG{Enabled: true, Cache: &cache},
		},
		Observe: observe.New(),
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/epg.xml", nil)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `<tv generator-info-name="Kiln"></tv>`) {
		t.Fatalf("empty EPG %d %s", response.Code, response.Body.String())
	}
}

func TestHLSPlayEndToEnd(t *testing.T) {
	var upstreamIndexQuery, upstreamMediaQuery url.Values
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live/index.m3u8":
			upstreamIndexQuery = r.URL.Query()
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nmedia.m3u8\n")
		case "/live/media.m3u8":
			upstreamMediaQuery = r.URL.Query()
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:1.0,\nseg0.ts\n#EXTINF:1.0,\nbroken.ts\n#EXTINF:1.0,\nempty-broken.ts\n")
		case "/live/seg0.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			_, _ = w.Write([]byte("FAKE-TS"))
		case "/live/broken.ts":
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: video/mp2t\r\nContent-Length: 12\r\n\r\nFAIL")
			_ = rw.Flush()
			_ = conn.Close()
		case "/live/empty-broken.ts":
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = rw.WriteString("HTTP/1.1 200 OK\r\nContent-Type: video/mp2t\r\nContent-Length: 12\r\n\r\n")
			_ = rw.Flush()
			_ = conn.Close()
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
		FFmpeg:  config.FFmpeg{Binary: "ffmpeg", HLSTime: 2, HLSListSize: 4},
		Observe: config.Observe{Enabled: true},
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
	expiredPlain, expiredRow, err := accesstoken.NewRow("expired", "", []string{"demo"})
	if err != nil {
		t.Fatal(err)
	}
	expiredRow.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if err := db.InsertAccessToken(expiredRow); err != nil {
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
	metrics, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metricsBody, _ := io.ReadAll(metrics.Body)
	_ = metrics.Body.Close()
	if metrics.StatusCode != http.StatusOK || !strings.Contains(string(metricsBody), "kiln_http_requests_total") {
		t.Fatalf("metrics %d %s", metrics.StatusCode, metricsBody)
	}

	adminPage, err := http.Get(ts.URL + "/admin/channels/demo")
	if err != nil {
		t.Fatal(err)
	}
	adminHTML, _ := io.ReadAll(adminPage.Body)
	_ = adminPage.Body.Close()
	if adminPage.StatusCode != http.StatusOK || !strings.Contains(string(adminHTML), "/admin/assets/app.js") {
		t.Fatalf("admin deep link %d %s", adminPage.StatusCode, adminHTML)
	}
	if csp := adminPage.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || strings.Contains(csp, "unsafe-inline") {
		t.Fatalf("unexpected content security policy: %q", csp)
	}
	expiredResp, err := http.Get(ts.URL + "/p/" + expiredPlain + "/playlist.m3u")
	if err != nil {
		t.Fatal(err)
	}
	_ = expiredResp.Body.Close()
	if expiredResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired access token status = %d", expiredResp.StatusCode)
	}

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
	wrongCredentialChange := adminJSON(t, http.MethodPut, ts.URL+"/v1/me/credentials", login.Token, map[string]string{
		"current_password": "wrong", "username": "admin", "new_password": "new-password",
	})
	if wrongCredentialChange.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(wrongCredentialChange.Body), "current_password_invalid") {
		t.Fatalf("wrong current password %d %s", wrongCredentialChange.StatusCode, wrongCredentialChange.Body)
	}
	oldToken := login.Token
	credentialChange := adminJSON(t, http.MethodPut, ts.URL+"/v1/me/credentials", login.Token, map[string]string{
		"current_password": "secret", "username": "admin", "new_password": "new-password",
	})
	if credentialChange.StatusCode != http.StatusOK {
		t.Fatalf("credential change %d %s", credentialChange.StatusCode, credentialChange.Body)
	}
	if err := json.Unmarshal(credentialChange.Body, &login); err != nil {
		t.Fatal(err)
	}
	oldSession := adminJSON(t, http.MethodGet, ts.URL+"/v1/me", oldToken, nil)
	if oldSession.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old credential token status = %d", oldSession.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/v1/play/demo/index.m3u8?token="+login.Token+"&_HLS_msn=7&_HLS_part=2&_HLS_skip=YES&ignored=value", nil)
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
	if got := upstreamIndexQuery.Encode(); got != "_HLS_msn=7&_HLS_part=2&_HLS_skip=YES" {
		t.Fatalf("upstream playlist query = %q", got)
	}

	var mediaURL string
	for _, line := range strings.Split(string(pb), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/v1/play/") {
			mediaURL = ts.URL + line
			break
		}
	}
	if mediaURL == "" {
		t.Fatalf("no media playlist url in %s", pb)
	}
	parsedMediaURL, err := url.Parse(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	mediaQuery := parsedMediaURL.Query()
	mediaQuery.Set("_HLS_msn", "8")
	mediaQuery.Set("_HLS_part", "0")
	mediaQuery.Set("_HLS_skip", "v2")
	mediaQuery.Set("ignored", "value")
	parsedMediaURL.RawQuery = mediaQuery.Encode()
	mediaResp, err := http.Get(parsedMediaURL.String())
	if err != nil {
		t.Fatal(err)
	}
	mediaBody, _ := io.ReadAll(mediaResp.Body)
	_ = mediaResp.Body.Close()
	if mediaResp.StatusCode != http.StatusOK {
		t.Fatalf("media playlist %d %s", mediaResp.StatusCode, mediaBody)
	}
	if got := upstreamMediaQuery.Encode(); got != "_HLS_msn=8&_HLS_part=0&_HLS_skip=v2" {
		t.Fatalf("upstream media query = %q", got)
	}

	var segmentURLs []string
	for _, line := range strings.Split(string(mediaBody), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "/v1/play/") {
			segmentURLs = append(segmentURLs, ts.URL+line)
		}
	}
	if len(segmentURLs) != 3 {
		t.Fatalf("no segment url in %s", mediaBody)
	}
	sresp, err := http.Get(segmentURLs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer sresp.Body.Close()
	sb, _ := io.ReadAll(sresp.Body)
	if sresp.StatusCode != 200 || string(sb) != "FAKE-TS" {
		t.Fatalf("segment %d %q", sresp.StatusCode, sb)
	}

	errorsBefore := obs.Snapshot().Errors
	brokenResp, err := http.Get(segmentURLs[1])
	if err == nil {
		_, readErr := io.ReadAll(brokenResp.Body)
		_ = brokenResp.Body.Close()
		if readErr == nil {
			t.Fatal("truncated upstream segment was reported as complete")
		}
	}
	if got := obs.Snapshot().Errors; got <= errorsBefore {
		t.Fatalf("errors after truncated segment = %d, want more than %d", got, errorsBefore)
	}

	emptyResp, err := http.Get(segmentURLs[2])
	if err != nil {
		t.Fatal(err)
	}
	emptyBody, _ := io.ReadAll(emptyResp.Body)
	_ = emptyResp.Body.Close()
	if emptyResp.StatusCode != http.StatusBadGateway || !strings.Contains(string(emptyBody), "read upstream body failed") {
		t.Fatalf("empty truncated segment = %d %s", emptyResp.StatusCode, emptyBody)
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

	disabled := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/channels/disable-all", login.Token, map[string]any{})
	if disabled.StatusCode != http.StatusOK || !strings.Contains(string(disabled.Body), `"changed":1`) {
		t.Fatalf("disable all %d %s", disabled.StatusCode, disabled.Body)
	}
	if _, running := sessions.Get("demo"); running {
		t.Fatal("disable-all left the channel session running")
	}
	disabledRow, ok, err := db.GetChannelRow("demo")
	if err != nil || !ok || !disabledRow.Channel.Disabled {
		t.Fatalf("disabled channel row = %#v, found=%v err=%v", disabledRow, ok, err)
	}
	enabled := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/channels/enable-all", login.Token, map[string]any{})
	if enabled.StatusCode != http.StatusOK || !strings.Contains(string(enabled.Body), `"changed":1`) {
		t.Fatalf("enable all %d %s", enabled.StatusCode, enabled.Body)
	}
	if _, running := sessions.Get("demo"); running {
		t.Fatal("enable-all unexpectedly started the channel session")
	}

	detail := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/channels/demo", login.Token, nil)
	if detail.StatusCode != http.StatusOK || !strings.Contains(string(detail.Body), `"effective_user_agent":"kiln-test"`) {
		t.Fatalf("channel detail %d %s", detail.StatusCode, detail.Body)
	}
	var detailBody struct {
		Channel  config.Channel `json:"channel"`
		Revision int64          `json:"revision"`
	}
	if err := json.Unmarshal(detail.Body, &detailBody); err != nil {
		t.Fatal(err)
	}
	detailBody.Channel.Title = "Updated Demo"
	detailBody.Channel.MaxViewers = 12
	detailBody.Channel.IdleTimeoutSec = 123
	detailBody.Channel.Headers = map[string]string{"Authorization": "Bearer retained-secret"}
	updated := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/channels/demo", login.Token, detailBody.Channel, map[string]string{"If-Match": strconv.FormatInt(detailBody.Revision, 10)})
	if updated.StatusCode != http.StatusOK {
		t.Fatalf("revision update %d %s", updated.StatusCode, updated.Body)
	}
	stale := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/channels/demo", login.Token, detailBody.Channel, map[string]string{"If-Match": strconv.FormatInt(detailBody.Revision, 10)})
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale revision status %d %s", stale.StatusCode, stale.Body)
	}
	importContent := "#EXTM3U\n#EXTINF:-1 tvg-id=\"demo\",Imported Demo\n" + origin.URL + "/live/index.m3u8?token=imported\n"
	importPreview := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/import/m3u", login.Token, map[string]any{
		"content": importContent, "apply": false,
	})
	if importPreview.StatusCode != http.StatusOK {
		t.Fatalf("m3u preview %d %s", importPreview.StatusCode, importPreview.Body)
	}
	var importPreviewBody catalog.ImportResult
	if err := json.Unmarshal(importPreview.Body, &importPreviewBody); err != nil || importPreviewBody.Updated != 1 || len(importPreviewBody.Entries) != 1 || importPreviewBody.Entries[0].Action != catalog.ImportUpdate {
		t.Fatalf("m3u preview = %#v, err=%v", importPreviewBody, err)
	}
	imported := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/import/m3u", login.Token, map[string]any{
		"content": importContent, "apply": true, "revisions": map[string]int64{"demo": detailBody.Revision + 1},
	})
	if imported.StatusCode != http.StatusOK {
		t.Fatalf("m3u import %d %s", imported.StatusCode, imported.Body)
	}
	importedRow, ok, err := db.GetChannelRow("demo")
	if err != nil || !ok {
		t.Fatalf("get imported channel: found=%v err=%v", ok, err)
	}
	if importedRow.Channel.MaxViewers != 12 || importedRow.Channel.IdleTimeoutSec != 123 || importedRow.Channel.Headers["Authorization"] != "Bearer retained-secret" {
		t.Fatalf("m3u import erased advanced settings: %#v", importedRow.Channel)
	}
	if importedRow.Channel.SourceURL != origin.URL+"/live/index.m3u8?token=imported" || importedRow.Channel.Upstream != "" || importedRow.Channel.Path != "" {
		t.Fatalf("m3u import did not preserve the direct source URL: %#v", importedRow.Channel)
	}
	staleImport := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/import/m3u", login.Token, map[string]any{
		"content": importContent, "apply": true, "revisions": map[string]int64{"demo": detailBody.Revision + 1},
	})
	if staleImport.StatusCode != http.StatusConflict {
		t.Fatalf("stale import %d %s", staleImport.StatusCode, staleImport.Body)
	}
	reordered := adminJSON(t, http.MethodPut, ts.URL+"/v1/admin/channels/reorder", login.Token, map[string]any{
		"ids": []string{"demo"}, "revisions": map[string]int64{"demo": importedRow.Revision},
	})
	if reordered.StatusCode != http.StatusOK {
		t.Fatalf("reorder %d %s", reordered.StatusCode, reordered.Body)
	}
	staleReorder := adminJSON(t, http.MethodPut, ts.URL+"/v1/admin/channels/reorder", login.Token, map[string]any{
		"ids": []string{"demo"}, "revisions": map[string]int64{"demo": importedRow.Revision},
	})
	if staleReorder.StatusCode != http.StatusConflict {
		t.Fatalf("stale reorder %d %s", staleReorder.StatusCode, staleReorder.Body)
	}

	preview := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/channels/demo/preview", login.Token, map[string]any{})
	if preview.StatusCode != http.StatusCreated {
		t.Fatalf("preview %d %s", preview.StatusCode, preview.Body)
	}
	var previewBody struct {
		PlayURL   string    `json:"play_url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(preview.Body, &previewBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(previewBody.PlayURL, "/v1/play/demo/index.m3u8?token=") || time.Until(previewBody.ExpiresAt) < 4*time.Minute {
		t.Fatalf("unexpected preview response: %+v", previewBody)
	}
	previewURL, err := url.Parse(previewBody.PlayURL)
	if err != nil {
		t.Fatal(err)
	}
	previewToken := previewURL.Query().Get("token")
	previewStatus := adminJSON(t, http.MethodGet, ts.URL+"/v1/status?token="+url.QueryEscape(previewToken), "", nil)
	if previewStatus.StatusCode != http.StatusUnauthorized {
		t.Fatalf("preview token reached general API: %d %s", previewStatus.StatusCode, previewStatus.Body)
	}
	previewPlay, err := http.Get(ts.URL + "/v1/play/demo/index.m3u8?token=" + url.QueryEscape(previewToken))
	if err != nil {
		t.Fatal(err)
	}
	_ = previewPlay.Body.Close()
	if previewPlay.StatusCode != http.StatusOK {
		t.Fatalf("preview token could not play scoped channel: %d", previewPlay.StatusCode)
	}

	warmup := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/channels/demo/warmup", login.Token, map[string]any{})
	if warmup.StatusCode != http.StatusAccepted {
		t.Fatalf("warmup %d %s", warmup.StatusCode, warmup.Body)
	}
	stop := adminJSON(t, http.MethodDelete, ts.URL+"/v1/admin/sessions/demo", login.Token, nil)
	if stop.StatusCode != http.StatusNoContent {
		t.Fatalf("stop %d %s", stop.StatusCode, stop.Body)
	}

	created := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/access-tokens", login.Token, map[string]any{
		"name": "temporary", "channel_ids": []string{"demo"}, "expires_in_sec": 86400,
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create access token %d %s", created.StatusCode, created.Body)
	}
	var createdBody struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(created.Body, &createdBody); err != nil {
		t.Fatal(err)
	}
	if createdBody.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("missing expiry: %+v", createdBody)
	}
	accessResp, err := http.Get(ts.URL + "/p/" + createdBody.Token + "/playlist.m3u")
	if err != nil {
		t.Fatal(err)
	}
	_ = accessResp.Body.Close()
	logs := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/access-logs?limit=10", login.Token, nil)
	if logs.StatusCode != http.StatusOK || strings.Contains(string(logs.Body), createdBody.Token) {
		t.Fatalf("access logs leaked token: %d %s", logs.StatusCode, logs.Body)
	}
	cleared := adminJSON(t, http.MethodDelete, ts.URL+"/v1/admin/access-logs", login.Token, nil)
	if cleared.StatusCode != http.StatusOK || !strings.Contains(string(cleared.Body), `"deleted":`) {
		t.Fatalf("clear logs %d %s", cleared.StatusCode, cleared.Body)
	}

	egressState := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/egress", login.Token, nil)
	var egressBody struct {
		Revision int64 `json:"revision"`
	}
	if egressState.StatusCode != http.StatusOK || json.Unmarshal(egressState.Body, &egressBody) != nil || egressBody.Revision == 0 {
		t.Fatalf("egress state %d %s", egressState.StatusCode, egressState.Body)
	}
	egressPut := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/egress", login.Token, map[string]any{
		"default": "proxy", "playlist_policy": "rewrite", "docker_proxy_host": "host.docker.internal",
		"proxies": []map[string]any{{"id": "proxy", "name": "Private", "url": "http://user:pass@127.0.0.1:18081"}},
		"rules":   []map[string]any{},
	}, map[string]string{"If-Match": strconv.FormatInt(egressBody.Revision, 10)})
	if egressPut.StatusCode != http.StatusOK {
		t.Fatalf("egress put %d %s", egressPut.StatusCode, egressPut.Body)
	}
	egressPublic := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/egress", login.Token, nil)
	if strings.Contains(string(egressPublic.Body), "user") || strings.Contains(string(egressPublic.Body), "pass") || !strings.Contains(string(egressPublic.Body), `"credential_configured":true`) {
		t.Fatalf("egress response leaked or lost credentials: %s", egressPublic.Body)
	}
	var refreshedEgress struct {
		Revision int64                   `json:"revision"`
		Proxies  []store.ProxyProfileRow `json:"proxies"`
	}
	if err := json.Unmarshal(egressPublic.Body, &refreshedEgress); err != nil {
		t.Fatal(err)
	}
	credentialPreservingPut := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/egress", login.Token, map[string]any{
		"default": "proxy", "playlist_policy": "rewrite", "docker_proxy_host": "host.docker.internal",
		"proxies": refreshedEgress.Proxies, "rules": []map[string]any{},
	}, map[string]string{"If-Match": strconv.FormatInt(refreshedEgress.Revision, 10)})
	if credentialPreservingPut.StatusCode != http.StatusOK {
		t.Fatalf("credential preserving put %d %s", credentialPreservingPut.StatusCode, credentialPreservingPut.Body)
	}
	storedProfiles, err := db.ListProxyProfiles()
	if err != nil || len(storedProfiles) != 1 || !strings.Contains(storedProfiles[0].URL, "user:pass@") {
		t.Fatalf("proxy credential was not preserved: %#v, %v", storedProfiles, err)
	}
	invalidProxy := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/egress", login.Token, map[string]any{
		"default": "bad", "playlist_policy": "rewrite",
		"proxies": []map[string]any{{"id": "bad", "url": "http://user:supersecret@[::1"}},
		"rules":   []map[string]any{},
	}, map[string]string{"If-Match": strconv.FormatInt(refreshedEgress.Revision+1, 10)})
	if invalidProxy.StatusCode != http.StatusBadRequest || strings.Contains(string(invalidProxy.Body), "supersecret") || strings.Contains(string(invalidProxy.Body), "user:") {
		t.Fatalf("invalid proxy leaked credentials: %d %s", invalidProxy.StatusCode, invalidProxy.Body)
	}
	staleEgress := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/egress", login.Token, map[string]any{
		"default": "direct", "playlist_policy": "rewrite", "proxies": []map[string]any{}, "rules": []map[string]any{},
	}, map[string]string{"If-Match": strconv.FormatInt(egressBody.Revision, 10)})
	if staleEgress.StatusCode != http.StatusConflict {
		t.Fatalf("stale egress status %d %s", staleEgress.StatusCode, staleEgress.Body)
	}
	settingsState := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/settings", login.Token, nil)
	var settingsBody struct {
		Revision int64 `json:"revision"`
	}
	if settingsState.StatusCode != http.StatusOK || json.Unmarshal(settingsState.Body, &settingsBody) != nil || settingsBody.Revision == 0 {
		t.Fatalf("settings state %d %s", settingsState.StatusCode, settingsState.Body)
	}
	settingsPut := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/settings", login.Token, map[string]string{
		"public_base_url": "https://new.kiln.test", "access_log_retention_days": "45",
	}, map[string]string{"If-Match": strconv.FormatInt(settingsBody.Revision, 10)})
	if settingsPut.StatusCode != http.StatusOK {
		t.Fatalf("settings put %d %s", settingsPut.StatusCode, settingsPut.Body)
	}
	staleSettings := adminJSONHeaders(t, http.MethodPut, ts.URL+"/v1/admin/settings", login.Token, map[string]string{
		"public_base_url": "https://stale.kiln.test", "access_log_retention_days": "10",
	}, map[string]string{"If-Match": strconv.FormatInt(settingsBody.Revision, 10)})
	if staleSettings.StatusCode != http.StatusConflict {
		t.Fatalf("stale settings status %d %s", staleSettings.StatusCode, staleSettings.Body)
	}

	_ = os.RemoveAll(filepath.Join(dir, "sessions"))
}

type responseBody struct {
	StatusCode int
	Body       []byte
}

func adminJSON(t *testing.T, method, rawURL, token string, body any) responseBody {
	return adminJSONHeaders(t, method, rawURL, token, body, nil)
}

func adminJSONHeaders(t *testing.T, method, rawURL, token string, body any, extraHeaders map[string]string) responseBody {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range extraHeaders {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return responseBody{StatusCode: resp.StatusCode, Body: data}
}
