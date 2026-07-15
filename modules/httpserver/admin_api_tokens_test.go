package httpserver_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/admintoken"
	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/store"
)

func TestAdminAPITokenScopesAndSessionOnlyManagement(t *testing.T) {
	dir := t.TempDir()
	hash, err := auth.HashPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.File{
		Server:   config.Server{DataDir: dir, PublicBaseURL: "http://kiln.test", ReadTimeout: 5, IdleTimeout: 30},
		Auth:     config.Auth{TokenIssuer: "kiln", TokenAudience: "kiln", Users: []config.User{{Username: "admin", PasswordHash: hash, Role: "admin"}}},
		Security: config.Security{MaxBodyBytes: 1 << 20},
		Egress:   config.Egress{Default: "direct", PlaylistPolicy: "rewrite"},
		Channels: []config.Channel{{ID: "demo", Title: "Demo", SourceURL: "https://example.com/live.m3u8", Ingress: "hls", OnDemand: true}},
	}
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.SeedFromConfig(cfg); err != nil {
		t.Fatal(err)
	}
	authService, err := auth.New(cfg.Auth, time.Hour, auth.Options{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	plain, row, err := admintoken.NewRow("Read robot", "", "admin", []string{"read"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertAdminAPIToken(row); err != nil {
		t.Fatal(err)
	}
	server := httpserver.New(httpserver.Deps{
		Cfg: cfg, Auth: authService, Catalog: catalog.New(cfg, db), Store: db, Observe: observe.New(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)

	read := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/channels", plain, nil)
	if read.StatusCode != http.StatusOK {
		t.Fatalf("read status=%d body=%s", read.StatusCode, read.Body)
	}
	write := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/channels/disable-all", plain, map[string]any{})
	if write.StatusCode != http.StatusForbidden {
		t.Fatalf("write status=%d body=%s", write.StatusCode, write.Body)
	}
	export := adminJSON(t, http.MethodPost, ts.URL+"/v1/admin/exports/m3u", plain, map[string]any{})
	if export.StatusCode != http.StatusForbidden {
		t.Fatalf("export status=%d body=%s", export.StatusCode, export.Body)
	}
	management := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/api-tokens", plain, nil)
	if management.StatusCode != http.StatusForbidden {
		t.Fatalf("token management status=%d body=%s", management.StatusCode, management.Body)
	}
	login, err := authService.Login("admin", "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	list := adminJSON(t, http.MethodGet, ts.URL+"/v1/admin/api-tokens", login.Token, nil)
	if list.StatusCode != http.StatusOK || !strings.Contains(string(list.Body), `"token_prefix":"`+row.Prefix+`"`) || strings.Contains(string(list.Body), plain) {
		t.Fatalf("session list status=%d body=%s", list.StatusCode, list.Body)
	}
	logs, err := db.ListAdminAPITokenLogs(10)
	if err != nil || len(logs) != 4 || logs[0].Reason != "session_required" || logs[1].Scope != "write" ||
		logs[1].Reason != "missing_scope" || logs[2].Reason != "missing_scope" || logs[3].Decision != "allow" {
		t.Fatalf("audit logs=%#v err=%v", logs, err)
	}
}
