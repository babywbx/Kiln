//go:build !windows

package egress

import "os"

func replaceFFmpegMPD(source, destination string) error {
	return os.Rename(source, destination)
}
