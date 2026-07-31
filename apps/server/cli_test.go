package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunRequiresExplicitConfig(t *testing.T) {
	t.Chdir(t.TempDir())

	if code := run(nil); code != 2 {
		t.Fatalf("run without -config returned %d, want 2", code)
	}
}

func TestRunAllowsVersionWithoutConfig(t *testing.T) {
	if code := run([]string{"-version"}); code != 0 {
		t.Fatalf("run -version returned %d, want 0", code)
	}
}

func TestRunAllowsHelpWithoutConfig(t *testing.T) {
	if code := run([]string{"-h"}); code != 0 {
		t.Fatalf("run -h returned %d, want 0", code)
	}
}

func TestRunAllowsHealthcheckWithoutConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	if code := run([]string{"-healthcheck", server.URL}); code != 0 {
		t.Fatalf("run -healthcheck returned %d, want 0", code)
	}
}
