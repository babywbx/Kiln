package egress

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/babywbx/kiln/modules/config"
)

func TestBuildPackagerArgsUsesEpochMicrosecondStartNumber(t *testing.T) {
	work := t.TempDir()
	args := buildPackagerArgs(DashOptions{
		HLSTime:     2,
		HLSListSize: 6,
	}, packAttempt{
		input: "input.mpd",
		vMap:  "0:v:0",
		aMap:  "0:a:0?",
	}, "00112233445566778899aabbccddeeff",
		filepath.Join(work, "index.m3u8"), filepath.Join(work, "seg_%05d.ts"))

	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "-hls_start_number_source\x00epoch_us") {
		t.Fatalf("ffmpeg args do not use a cross-restart start number: %q", args)
	}
	for i, arg := range args {
		if arg == "-protocol_whitelist" && (i+1 >= len(args) || args[i+1] != "file,crypto") {
			t.Fatalf("ffmpeg network protocols are enabled: %q", args)
		}
	}
}

func TestBuildPackagerArgsForcesGuardedNetworkProxy(t *testing.T) {
	work := t.TempDir()
	args := buildPackagerArgs(DashOptions{
		HLSTime:     2,
		HLSListSize: 6,
		SourceURL:   "https://media.example/live.mpd",
	}, packAttempt{
		input:    "https://media.example/live.mpd",
		vMap:     "0:v:0",
		aMap:     "0:a:0?",
		network:  true,
		proxyURL: "http://kiln:secret@127.0.0.1:1234",
		headers:  map[string]string{"Authorization": "Bearer secret"},
	}, "00112233445566778899aabbccddeeff",
		filepath.Join(work, "index.m3u8"), filepath.Join(work, "seg_%05d.ts"))
	joined := strings.Join(args, "\x00")
	for _, want := range []string{
		"-protocol_whitelist\x00file,http,https,tcp,tls,crypto,httpproxy",
		"-max_redirects\x000",
		"-http_proxy\x00http://kiln:secret@127.0.0.1:1234",
		"-headers\x00Authorization: Bearer secret\r\n",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("network args missing %q: %q", want, args)
		}
	}
}

func TestBuildPackagerArgsAllowsRedirectsForInsecureUpgrade(t *testing.T) {
	work := t.TempDir()
	args := buildPackagerArgs(DashOptions{
		HLSTime: 2, HLSListSize: 6, UpgradeInsecureRedirects: true,
	}, packAttempt{
		input: "https://media.example/live.mpd", vMap: "0:v:0", aMap: "0:a:0?", network: true,
	}, "00112233445566778899aabbccddeeff",
		filepath.Join(work, "index.m3u8"), filepath.Join(work, "seg_%05d.ts"))
	if joined := strings.Join(args, "\x00"); !strings.Contains(joined, "-max_redirects\x008") {
		t.Fatalf("guarded redirect limit missing: %q", args)
	}
}

func TestBuildPackagerArgsKeepsHTTPOptionsOffAFileInput(t *testing.T) {
	work := t.TempDir()
	args := buildPackagerArgs(DashOptions{
		HLSTime: 2, HLSListSize: 6, UserAgent: "Kiln/1", SourceURL: "https://media.example/live.mpd",
	}, packAttempt{
		input:    filepath.Join(work, "input.mpd"),
		vMap:     "0:v:0",
		aMap:     "0:a:0?",
		network:  true,
		proxyURL: "http://127.0.0.1:1234",
		headers:  map[string]string{"Authorization": "Bearer secret"},
	}, "00112233445566778899aabbccddeeff",
		filepath.Join(work, "index.m3u8"), filepath.Join(work, "seg_%05d.ts"))
	joined := strings.Join(args, "\x00")
	for _, rejected := range []string{"-max_redirects", "-reconnect", "-http_proxy", "-user_agent", "-headers"} {
		if strings.Contains(joined, rejected) {
			t.Fatalf("%s reaches a file input, and ffmpeg refuses the whole run over it: %q", rejected, args)
		}
	}
	if !strings.Contains(joined, "-protocol_whitelist\x00file,http,https,tcp,tls,crypto,httpproxy") {
		t.Fatalf("segments still travel over the network: %q", args)
	}
}

func TestCanUpgradeFFmpegHTTPRedirects(t *testing.T) {
	if !canUpgradeFFmpegHTTPRedirects(`<MPD xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><BaseURL>https://cdn.example/live/</BaseURL></MPD>`) {
		t.Fatal("HTTPS-only MPD rejected redirect upgrades")
	}
	if canUpgradeFFmpegHTTPRedirects(`<MPD><BaseURL>http://cdn.example/live/</BaseURL></MPD>`) {
		t.Fatal("explicit HTTP media URL accepted redirect upgrades")
	}
}

func TestPlanFFmpegCommandNative(t *testing.T) {
	args := []string{"-i", "https://example.test/live.mpd", "out.m3u8"}
	proxyEnv := []string{"HTTPS_PROXY=http://127.0.0.1:7890"}
	plan, err := planFFmpegCommand(DashOptions{
		Binary: "ffmpeg", FFmpegMode: config.FFmpegModeNative,
	}, t.TempDir(), args, proxyEnv, "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.executable != "ffmpeg" || !reflect.DeepEqual(plan.args, args) {
		t.Fatalf("native plan: %+v", plan)
	}
	if !reflect.DeepEqual(plan.env, proxyEnv) || plan.containerName != "" {
		t.Fatalf("native environment: %+v", plan)
	}
}

func TestPlanFFmpegCommandDocker(t *testing.T) {
	work := t.TempDir()
	input := filepath.Join(work, "input.mpd")
	output := filepath.Join(work, "index.m3u8")
	plan, err := planFFmpegCommand(DashOptions{
		FFmpegMode:  config.FFmpegModeDocker,
		DockerImage: "kiln:test",
	}, work, []string{"-i", input, output}, []string{
		"HTTP_PROXY=http://host.docker.internal:7890",
	}, "kiln-ff-test")
	if err != nil {
		t.Fatal(err)
	}
	if plan.executable != "docker" || plan.containerName != "kiln-ff-test" {
		t.Fatalf("docker plan: %+v", plan)
	}
	joined := strings.Join(plan.args, "\x00")
	for _, want := range []string{
		"run", "--rm", "--name", "kiln-ff-test",
		"--add-host", "host.docker.internal:host-gateway",
		"--entrypoint", "/usr/local/bin/ffmpeg",
		"type=bind,source=" + work + ",target=/work",
		"HTTP_PROXY=http://host.docker.internal:7890",
		"kiln:test", "/work/input.mpd", "/work/index.m3u8",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("docker args missing %q: %q", want, plan.args)
		}
	}
	if len(plan.env) != 0 {
		t.Fatalf("proxy environment must be passed through docker -e: %q", plan.env)
	}
}
