package httpserver

import (
	"io"
	"time"
)

const (
	probeSampleBytes   = 4 << 20
	probeSampleWindow  = 5 * time.Second
	probeSampleMinimum = 256 << 10
	probeDefaultFloor  = 1000
)

type probeSample struct {
	bytes      int64
	firstByte  time.Duration
	transfer   time.Duration
	measurable bool
	kbps       int64
}

// A reachable destination can still be too slow to carry media, and a small
// response cannot tell us either way, so throughput is only reported when the
// sample is large enough to mean something.
func sampleProbeBody(body io.Reader) probeSample {
	started := time.Now()
	sample := probeSample{}
	buffer := make([]byte, 64<<10)
	deadline := started.Add(probeSampleWindow)
	firstByteSeen := false

	for sample.bytes < probeSampleBytes && time.Now().Before(deadline) {
		read, err := body.Read(buffer)
		if read > 0 {
			if !firstByteSeen {
				sample.firstByte = time.Since(started)
				firstByteSeen = true
			}
			sample.bytes += int64(read)
		}
		if err != nil {
			break
		}
	}
	sample.transfer = time.Since(started) - sample.firstByte
	if sample.bytes >= probeSampleMinimum && sample.transfer > 0 {
		sample.measurable = true
		sample.kbps = sample.bytes * 8 / sample.transfer.Milliseconds()
	}
	return sample
}

func probeThroughputFloor(configured int) int64 {
	switch {
	case configured < 0:
		return 0
	case configured == 0:
		return probeDefaultFloor
	default:
		return int64(configured)
	}
}
