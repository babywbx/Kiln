package httpserver_test

import (
	"bytes"
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
	imported := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/import/m3u", login.Token, map[string]any{
		"apply": true, "revisions": map[string]int64{"demo": detailBody.Revision + 1},
		"entries": []map[string]any{{
			"title": "Imported Demo", "suggested_id": "demo", "suggested_upstream": "origin",
			"suggested_path": "/live/index.m3u8", "suggested_ingress": "hls",
		}},
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
	staleImport := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/import/m3u", login.Token, map[string]any{
		"apply": true, "revisions": map[string]int64{"demo": detailBody.Revision + 1},
		"entries": []map[string]any{{
			"title": "Stale Import", "suggested_id": "demo", "suggested_upstream": "origin",
			"suggested_path": "/live/index.m3u8", "suggested_ingress": "hls",
		}},
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
