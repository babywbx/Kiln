//go:build !linux

package filecache

import "os"

func DropAfterWrite(*os.File) error { return nil }

func DropAfterRead(*os.File) {}
