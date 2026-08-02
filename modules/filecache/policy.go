package filecache

import "sync/atomic"

var enabled atomic.Bool

func SetEnabled(value bool) {
	enabled.Store(value)
}

func Enabled() bool {
	return enabled.Load()
}
