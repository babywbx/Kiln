package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadTOMLAndJSONC(t *testing.T) {
	dir := t.TempDir()
	tomlPath := filepath.Join(dir, "kiln.toml")
	body := `
[server]
listen = "0.0.0.0:8080"
public_base_url = "http://127.0.0.1:8080"
data_dir = "./data"

[auth]
token_ttl_hours = 1

[epg]
enabled = true
cache = false
refresh_interval_min = 30
max_source_bytes = 1048576
default_timezone = "Asia/Hong_Kong"

[[epg.sources]]
id = "hk-1"
enabled = true
proxy = "auto"

[[auth.users]]
username = "admin"
password_hash = "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie"
role = "admin"

[[upstreams]]
id = "origin"
base_url = "http://127.0.0.1:5050"

[[channels]]
id = "ch1"
title = "One"
upstream = "origin"
path = "/a/b"
ingress = "hls"
on_demand = true
preferred_audio_languages = ["yue", "zh"]
`
	if err := os.WriteFile(tomlPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(tomlPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "0.0.0.0:8080" {
		t.Fatalf("listen %s", cfg.Server.Listen)
	}
	if len(cfg.Channels) != 1 || cfg.Channels[0].ID != "ch1" {
		t.Fatalf("channels %+v", cfg.Channels)
	}
	if got := cfg.Channels[0].PreferredAudioLanguages; len(got) != 2 || got[0] != "yue" || got[1] != "zh" {
		t.Fatalf("preferred audio languages = %v", got)
	}
	if cfg.FFmpeg.Mode != FFmpegModeNative || cfg.FFmpeg.DockerImage != "kiln:local" {
		t.Fatalf("ffmpeg defaults: %+v", cfg.FFmpeg)
	}
	if cfg.FFmpeg.Dependency() != "ffmpeg" || cfg.FFmpeg.Mode.IsDocker() {
		t.Fatalf("native ffmpeg behavior: %+v", cfg.FFmpeg)
	}
	if cfg.Packager.PartTargetMS != 500 {
		t.Fatalf("part target default = %d, want 500", cfg.Packager.PartTargetMS)
	}
	invalidPartTarget := cfg
	invalidPartTarget.Packager.PartTargetMS = 20
	if err := invalidPartTarget.validate(); err == nil {
		t.Fatal("invalid LL-HLS part target accepted")
	}
	if !cfg.EPG.Enabled || cfg.EPG.CacheEnabled() || cfg.EPG.RefreshIntervalMin != 30 {
		t.Fatalf("epg config: %+v", cfg.EPG)
	}
	if cfg.EPG.CacheDir != filepath.Join(cfg.Server.DataDir, "epg") || len(cfg.EPG.Sources) != 1 || cfg.EPG.Sources[0].ID != "hk-1" {
		t.Fatalf("epg defaults/sources: %+v", cfg.EPG)
	}
	dockerCfg := cfg
	dockerCfg.FFmpeg.Mode = FFmpegModeDocker
	if err := dockerCfg.validate(); err != nil {
		t.Fatalf("docker ffmpeg mode rejected: %v", err)
	}
	if dockerCfg.FFmpeg.Dependency() != "docker" || !dockerCfg.FFmpeg.Mode.IsDocker() {
		t.Fatalf("docker ffmpeg behavior: %+v", dockerCfg.FFmpeg)
	}
	invalidCfg := cfg
	invalidCfg.FFmpeg.Mode = "sidecar"
	if err := invalidCfg.validate(); err == nil {
		t.Fatal("invalid ffmpeg mode accepted")
	}
	directSourceCfg := cfg
	directSourceCfg.Channels = []Channel{{
		ID: "direct", SourceURL: "https://media.example/live/index.m3u8?token=abc", Ingress: "hls",
	}}
	if err := directSourceCfg.validate(); err != nil {
		t.Fatalf("direct source URL rejected: %v", err)
	}
	directSourceCfg.Channels[0].SourceURL = "ftp://media.example/live/index.m3u8"
	if err := directSourceCfg.validate(); err == nil {
		t.Fatal("non-HTTP source URL accepted")
	}

	jsoncPath := filepath.Join(dir, "kiln.jsonc")
	jc := `{
  "server": { "listen": "0.0.0.0:8081", "public_base_url": "http://127.0.0.1:8081", "data_dir": "./data" },
  "auth": {
    "users": [{ "username": "admin", "password_hash": "$2a$10$8JxhvnpdTX/TrOTi1XaYWuPlrZK1aw3ANgGIWpTO6KtD2M432w7Ie", "role": "admin" }],
  },
  "upstreams": [{ "id": "origin", "base_url": "http://127.0.0.1:5050" }],
  "channels": [{ "id": "c2", "upstream": "origin", "path": "/x", "ingress": "hls", "on_demand": true }],
}`
	if err := os.WriteFile(jsoncPath, []byte(jc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(jsoncPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Server.Listen != "0.0.0.0:8081" || cfg2.Channels[0].ID != "c2" {
		t.Fatalf("%+v", cfg2)
	}
}

func TestValidateChannelID(t *testing.T) {
	for _, id := range []string{"channel-1", "news-hd", "channel_2", "sports.us", "频道", "news hd", "~new", " news", strings.Repeat("a", 300)} {
		if err := ValidateChannelID(id); err != nil {
			t.Fatalf("ValidateChannelID(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"", ".", "..", "news/hd", `news\hd`, "news\x00hd"} {
		if err := ValidateChannelID(id); err == nil {
			t.Fatalf("ValidateChannelID(%q) succeeded", id)
		}
	}
}

func TestStripJSONCTrailingComma(t *testing.T) {
	in := []byte(`{"a":1,}`)
	out := StripJSONC(in)
	if string(out) != `{"a":1}` && string(out) != "{\n\"a\":1\n}" {
		if !containsByte(out, '"') {
			t.Fatalf("%s", out)
		}
	}
}

func containsByte(b []byte, c byte) bool {
	for _, x := range b {
		if x == c {
			return true
		}
	}
	return false
}
