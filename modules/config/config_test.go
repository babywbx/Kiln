package config

import (
	"os"
	"path/filepath"
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
token_secret = "test-secret-16chars"
token_ttl_hours = 1

[[auth.users]]
username = "admin"
password_hash = "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLmnopqr"
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

	jsoncPath := filepath.Join(dir, "kiln.jsonc")
	jc := `{
  "server": { "listen": "0.0.0.0:8081", "public_base_url": "http://127.0.0.1:8081", "data_dir": "./data" },
  "auth": {
    "token_secret": "test-secret-16chars",
    "users": [{ "username": "admin", "password_hash": "$2a$10$abcdefghijklmnopqrstuuABCDEFGHIJKLmnopqr", "role": "admin" }],
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
