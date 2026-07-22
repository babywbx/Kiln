//go:build !linux || !lite

package filecache

import "os"

func DropAfterWrite(*os.File) error { return nil }

func DropAfterRead(*os.File) {}
