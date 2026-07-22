package filecache

import "sync/atomic"

var enabled atomic.Bool

func init() {
	enabled.Store(true)
}

// SetEnabled controls the process-wide Lite page-cache policy.
func SetEnabled(value bool) {
	enabled.Store(value)
}

func Enabled() bool {
	return enabled.Load()
}
