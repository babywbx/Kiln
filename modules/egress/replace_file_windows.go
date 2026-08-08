//go:build windows

package egress

import "golang.org/x/sys/windows"

func replaceFFmpegMPD(source, destination string) error {
	return windows.Rename(source, destination)
}
