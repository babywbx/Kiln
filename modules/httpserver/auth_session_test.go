//go:build !lite

package httpserver_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
)

func newAuthSessionServer(t *testing.T) (*httptest.Server, *auth.Service, *observe.Service) {
	t.Helper()
	hash, err := auth.HashPassword("secret-password")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.File{
		Server: config.Server{PublicBaseURL: "http://kiln.test", DataDir: t.TempDir(), ReadTimeout: 5, IdleTimeout: 30},
		Auth: config.Auth{TokenIssuer: "kiln", TokenAudience: "kiln", LoginRatePerMin: 100, Users: []config.User{
			{Username: "admin", PasswordHash: hash, Role: "admin"},
			{Username: "viewer", PasswordHash: hash, Role: "viewer", ChannelIDs: []string{"one"}},
		}},
		Security: config.Security{MaxBodyBytes: 1 << 20},
	}
	authService, err := auth.New(cfg.Auth, time.Hour, auth.Options{DataDir: cfg.Server.DataDir})
	if err != nil {
		t.Fatal(err)
	}
	observed := observe.New()
	server := httpserver.New(httpserver.Deps{
		Cfg: cfg, Auth: authService, Observe: observed,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	testServer := httptest.NewServer(server.Handler())
	t.Cleanup(testServer.Close)
	return testServer, authService, observed
}

func TestAdminAPIRejectsQueryToken(t *testing.T) {
	testServer, authService, _ := newAuthSessionServer(t)
	login, err := authService.Login("admin", "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	queryResponse, err := http.Get(testServer.URL + "/v1/me?token=" + url.QueryEscape(login.Token))
	if err != nil {
		t.Fatal(err)
	}
	_ = queryResponse.Body.Close()
	if queryResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token status = %d", queryResponse.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/v1/me", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	bearerResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = bearerResponse.Body.Close()
	if bearerResponse.StatusCode != http.StatusOK {
		t.Fatalf("bearer token status = %d", bearerResponse.StatusCode)
	}
}

func TestScopedStatusOnlyIncludesAuthorizedChannels(t *testing.T) {
	testServer, authService, observed := newAuthSessionServer(t)
	observed.UpsertSession(observe.SessionStat{ChannelID: "one", State: "running", LastError: "allowed"})
	observed.UpsertSession(observe.SessionStat{ChannelID: "two", State: "failed", LastError: "private"})
	login, err := authService.Login("viewer", "secret-password")
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, testServer.URL+"/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+login.Token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot observe.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || snapshot.SessionCount != 1 || len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ChannelID != "one" {
		t.Fatalf("scoped snapshot = %+v", snapshot)
	}
}
