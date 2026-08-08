package security

import (
	"strconv"
	"testing"
)

func TestLimiterBoundsUniqueBuckets(t *testing.T) {
	const maxBuckets = 65536
	limiter := NewLimiter(20)
	for i := 0; i <= maxBuckets; i++ {
		limiter.Allow(strconv.Itoa(i))
	}
	if got := len(limiter.buckets); got > maxBuckets {
		t.Fatalf("unique buckets = %d, want at most %d", got, maxBuckets)
	}
}
