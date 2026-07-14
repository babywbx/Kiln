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
