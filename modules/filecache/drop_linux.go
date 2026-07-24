//go:build linux

// Package filecache keeps constrained media workloads from filling a cgroup
// with reclaimable page cache.
package filecache

import (
	"os"

	"golang.org/x/sys/unix"
)

func DropAfterWrite(file *os.File) error {
	if !Enabled() {
		return nil
	}
	if err := file.Sync(); err != nil {
		return err
	}
	return unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_DONTNEED)
}

func DropAfterRead(file *os.File) {
	if !Enabled() {
		return
	}
	_ = unix.Fadvise(int(file.Fd()), 0, 0, unix.FADV_DONTNEED)
}
