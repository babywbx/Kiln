package egress

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/babywbx/kiln/modules/config"
)

type ffmpegCommandPlan struct {
	executable    string
	args          []string
	env           []string
	containerName string
}

func planFFmpegCommand(opt DashOptions, absWork string, ffmpegArgs, proxyEnv []string, containerName string) (ffmpegCommandPlan, error) {
	switch opt.FFmpegMode {
	case "", config.FFmpegModeNative:
		if opt.Binary == "" {
			return ffmpegCommandPlan{}, fmt.Errorf("ffmpeg binary is required")
		}
		return ffmpegCommandPlan{
			executable: opt.Binary,
			args:       append([]string(nil), ffmpegArgs...),
			env:        append([]string(nil), proxyEnv...),
		}, nil
	case config.FFmpegModeDocker:
		if opt.DockerImage == "" {
			return ffmpegCommandPlan{}, fmt.Errorf("ffmpeg docker image is required")
		}
		if containerName == "" {
			return ffmpegCommandPlan{}, fmt.Errorf("ffmpeg container name is required")
		}
		args := []string{
			"run", "--rm",
			"--name", containerName,
			"--label", "kiln.ffmpeg=1",
			"--entrypoint", "/usr/local/bin/ffmpeg",
			"--mount", "type=bind,source=" + absWork + ",target=/work",
			"--workdir", "/work",
		}
		args = append(args, dockerUserArgs()...)
		for _, item := range proxyEnv {
			if strings.Contains(item, "=") {
				args = append(args, "-e", item)
			}
		}
		args = append(args, opt.DockerImage)
		for _, arg := range ffmpegArgs {
			args = append(args, dockerWorkPath(arg, absWork))
		}
		return ffmpegCommandPlan{
			executable:    "docker",
			args:          args,
			containerName: containerName,
		}, nil
	default:
		return ffmpegCommandPlan{}, fmt.Errorf("unsupported ffmpeg mode %q", opt.FFmpegMode)
	}
}

func dockerWorkPath(value, absWork string) string {
	cleanWork := filepath.Clean(absWork)
	cleanValue := filepath.Clean(value)
	if cleanValue == cleanWork {
		return "/work"
	}
	prefix := cleanWork + string(filepath.Separator)
	if !strings.HasPrefix(cleanValue, prefix) {
		return value
	}
	rel, err := filepath.Rel(cleanWork, cleanValue)
	if err != nil {
		return value
	}
	return path.Join("/work", filepath.ToSlash(rel))
}
