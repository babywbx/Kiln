package filecache

import "sync/atomic"

var enabled atomic.Bool

// SetEnabled controls the process-wide media page-cache policy.
func SetEnabled(value bool) {
	enabled.Store(value)
}

func Enabled() bool {
	return enabled.Load()
}
