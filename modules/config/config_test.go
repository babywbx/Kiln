package config

import (
	"encoding/json"
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
	if cfg.EPG.Sources[0].Proxy != "direct" {
		t.Fatalf("EPG source default proxy = %q, want direct", cfg.EPG.Sources[0].Proxy)
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

func TestDefaultPackagerEngineOnlyFillsAnEmptyConfigValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	base := `
[server]
data_dir = "./data"

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"
`

	t.Setenv("KILN_DEFAULT_PACKAGER_ENGINE", EngineNative)
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Packager.Engine != EngineNative {
		t.Fatalf("default engine = %q, want %q", cfg.Packager.Engine, EngineNative)
	}

	explicit := base + `
[packager]
engine = "auto"
`
	if err := os.WriteFile(path, []byte(explicit), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Packager.Engine != EngineAuto {
		t.Fatalf("explicit engine = %q, want %q", cfg.Packager.Engine, EngineAuto)
	}
}

func TestLoadPreloadsGlobalKeysBesideConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	keysPath := filepath.Join(dir, "kiln.keys")
	if err := os.WriteFile(keysPath, []byte(
		"00112233445566778899aabbccddeeff:ffeeddccbbaa99887766554433221100\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	config := `
[server]
data_dir = "./data"

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"

[packager]
keys_file = "kiln.keys"

[[upstreams]]
id = "origin"
base_url = "https://example.com"

[[channels]]
id = "dash"
upstream = "origin"
path = "/live.mpd"
ingress = "dash"
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Packager.KeysFile != keysPath {
		t.Fatalf("packager keys_file = %q, want %q", cfg.Packager.KeysFile, keysPath)
	}
	keys := cfg.GlobalKeys()
	if len(keys) != 1 || keys[0].KID != "00112233445566778899aabbccddeeff" {
		t.Fatalf("global keys = %+v", keys)
	}
	keys[0].Key = "changed"
	again := cfg.GlobalKeys()
	if again[0].Key != "ffeeddccbbaa99887766554433221100" {
		t.Fatal("GlobalKeys exposed mutable key storage")
	}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ffeeddccbbaa99887766554433221100") {
		t.Fatal("serialized config leaked a global key")
	}
}

func TestLoadRejectsMissingGlobalKeysFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	config := `
[server]
data_dir = "./data"

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"

[packager]
keys_file = "kiln.keys"
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("missing global keys file was accepted")
	}
	if !strings.Contains(err.Error(), "packager.keys_file") {
		t.Fatalf("error = %q, want packager.keys_file context", err)
	}
}

func TestResourceModeDefaultsToAutoAndAllowsPerformanceOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	base := `
[server]
data_dir = "./data"

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"
`
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ResourceMode != ResourceModeAuto {
		t.Fatalf("default resource mode = %q, want %q", cfg.Server.ResourceMode, ResourceModeAuto)
	}

	t.Setenv("KILN_RESOURCE_MODE", ResourceModePerformance)
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ResourceMode != ResourceModePerformance {
		t.Fatalf("environment resource mode = %q, want %q", cfg.Server.ResourceMode, ResourceModePerformance)
	}

	t.Setenv("KILN_RESOURCE_MODE", "unlimited-ish")
	if _, err := Load(path); err == nil {
		t.Fatal("invalid resource mode was accepted")
	}
}

func TestNegativeServerMemoryLimitIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	config := `
[server]
memory_limit_mb = -1

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("negative server.memory_limit_mb was accepted")
	}
}

func TestServerMemoryLimitThatCannotBeRepresentedAsBytesIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	config := `
[server]
memory_limit_mb = 9223372036854775807

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"
`
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("overflowing server.memory_limit_mb was accepted")
	}
}

func TestInvalidPackagerDefaultsAndExplicitValuesAreRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "kiln.toml")
	base := `
[server]
data_dir = "./data"

[auth]
[[auth.users]]
username = "admin"
password_hash = "hash"
role = "admin"
`

	t.Setenv("KILN_DEFAULT_PACKAGER_ENGINE", "sidecar")
	if err := os.WriteFile(path, []byte(base), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("invalid default packager engine was accepted")
	}

	t.Setenv("KILN_DEFAULT_PACKAGER_ENGINE", EngineNative)
	explicit := base + `
[packager]
engine = "sidecar"
`
	if err := os.WriteFile(path, []byte(explicit), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("invalid explicit packager engine was accepted")
	}
}

func TestDebugPprofRequiresExplicitLoopbackListener(t *testing.T) {
	base := File{
		Server: Server{DataDir: t.TempDir()},
		Auth: Auth{Users: []User{{
			Username: "admin", PasswordHash: "hash", Role: "admin",
		}}},
	}

	disabled := base
	disabled.applyDefaults()
	if disabled.Debug.Pprof.Enabled {
		t.Fatal("pprof enabled by default")
	}
	if err := disabled.validate(); err != nil {
		t.Fatalf("disabled pprof rejected: %v", err)
	}

	for _, listen := range []string{"127.0.0.1:6060", "[::1]:6060"} {
		cfg := base
		cfg.Debug.Pprof.Enabled = true
		cfg.Debug.Pprof.Listen = listen
		cfg.applyDefaults()
		if err := cfg.validate(); err != nil {
			t.Fatalf("loopback listener %q rejected: %v", listen, err)
		}
	}

	for _, listen := range []string{"0.0.0.0:6060", "[::]:6060", "192.168.1.10:6060", "localhost:6060"} {
		cfg := base
		cfg.Debug.Pprof.Enabled = true
		cfg.Debug.Pprof.Listen = listen
		cfg.applyDefaults()
		if err := cfg.validate(); err == nil {
			t.Fatalf("non-loopback IP listener %q accepted", listen)
		}
	}
}

func TestValidateChannelID(t *testing.T) {
	for _, id := range []string{"channel1", "news-hd", "channel_2", "sports.us", "频道", "news hd", "~new", " news", strings.Repeat("a", 300)} {
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

func TestValidateEngineSelectionRejectsExplicitFFmpegSubtitles(t *testing.T) {
	selection := TrackSelection{Subtitles: SubtitleSelection{
		Mode:  "only",
		Track: TrackSelector{RepresentationID: "sub-zh"},
	}}
	if err := ValidateEngineSelection(EngineFFmpeg, selection); err == nil {
		t.Fatal("explicit FFmpeg subtitle selection was accepted")
	}
	if err := ValidateEngineSelection(EngineAuto, selection); err != nil {
		t.Fatalf("auto engine rejected selection: %v", err)
	}
	selection.Subtitles.Mode = "off"
	if err := ValidateEngineSelection(EngineFFmpeg, selection); err != nil {
		t.Fatalf("FFmpeg subtitle-off selection rejected: %v", err)
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
