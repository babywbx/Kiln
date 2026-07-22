//go:build lite

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

func TestValidateLiteConfigRejectsExcludedFeatures(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.File
		want string
	}{
		{
			name: "global ffmpeg engine",
			cfg: config.File{Packager: config.Packager{Engine: config.EngineFFmpeg},
				Channels: []config.Channel{{ID: "news"}}},
			want: "must be native",
		},
		{name: "global auto engine", cfg: config.File{Packager: config.Packager{Engine: config.EngineAuto}}, want: "must be native"},
		{
			name: "disabled channel override",
			cfg: config.File{Packager: config.Packager{Engine: config.EngineNative},
				Channels: []config.Channel{{ID: "news", Packager: config.EngineFFmpeg, Disabled: true}}},
			want: "requires native",
		},
		{
			name: "epg",
			cfg:  config.File{Packager: config.Packager{Engine: config.EngineNative}, EPG: config.EPG{Enabled: true}},
			want: "epg is not available",
		},
		{
			name: "otlp",
			cfg: config.File{Packager: config.Packager{Engine: config.EngineNative},
				Observe: config.Observe{OTLPEndpoint: "http://collector"}},
			want: "OpenTelemetry",
		},
		{
			name: "pprof",
			cfg: config.File{Packager: config.Packager{Engine: config.EngineNative},
				Debug: config.Debug{Pprof: config.Pprof{Enabled: true}}},
			want: "pprof",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateLiteConfig(test.cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateLiteConfig() = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestValidateLiteConfigAcceptsNativeChannels(t *testing.T) {
	cfg := config.File{
		Packager: config.Packager{Engine: config.EngineNative},
		Channels: []config.Channel{{ID: "news"}},
	}
	if err := validateLiteConfig(cfg); err != nil {
		t.Fatalf("validateLiteConfig() = %v", err)
	}
}

func TestRunReturnsFailureWhenListenAddressIsUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("KILN_PLAY_OPEN", "1")

	directory := t.TempDir()
	configPath := filepath.Join(directory, "kiln.toml")
	configText := fmt.Sprintf(`
[server]
listen = %q
data_dir = %q

[auth]
[[auth.users]]
username = "admin"
password_hash = "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie"
role = "admin"

[packager]
engine = "native"

[epg]
enabled = false
`, listener.Addr().String(), directory)
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan int, 1)
	go func() {
		done <- run([]string{"-config", configPath})
	}()
	select {
	case code := <-done:
		if code == 0 {
			t.Fatal("run succeeded with an unavailable listen address")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run waited for a signal after listen failed")
	}
}
