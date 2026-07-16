package debugserver_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/debugserver"
)

func TestNewKeepsPprofDisabledUnlessExplicitlyEnabled(t *testing.T) {
	server, err := debugserver.New(config.Pprof{}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if server != nil {
		t.Fatal("disabled pprof created a server")
	}
}

func TestNewServesPprofOnDedicatedHandler(t *testing.T) {
	server, err := debugserver.New(config.Pprof{
		Enabled: true,
		Listen:  "127.0.0.1:6060",
	}, discardLogger())
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("pprof index status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
}

func TestNewRejectsNonLoopbackListener(t *testing.T) {
	_, err := debugserver.New(config.Pprof{
		Enabled: true,
		Listen:  "0.0.0.0:6060",
	}, discardLogger())
	if err == nil {
		t.Fatal("public pprof listener accepted")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
