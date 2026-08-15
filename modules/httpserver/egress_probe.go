package httpserver

import (
	"errors"
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

// Small responses are measurable only after the window expires.
func sampleProbeBody(body io.ReadCloser, requestStarted time.Time) (probeSample, error) {
	return sampleProbeBodyWithin(body, requestStarted, probeSampleWindow)
}

func sampleProbeBodyWithin(body io.ReadCloser, requestStarted time.Time, window time.Duration) (probeSample, error) {
	sampleStarted := time.Now()
	firstByteAt := sampleStarted
	sample := probeSample{}
	buffer := make([]byte, 64<<10)
	firstByteSeen := false
	var readErr error
	expired := make(chan struct{})
	timer := time.AfterFunc(window, func() {
		_ = body.Close()
		close(expired)
	})

	for sample.bytes < probeSampleBytes {
		remaining := probeSampleBytes - sample.bytes
		read, err := body.Read(buffer[:min(int64(len(buffer)), remaining)])
		if read > 0 {
			if !firstByteSeen {
				firstByteAt = time.Now()
				sample.firstByte = firstByteAt.Sub(requestStarted)
				firstByteSeen = true
			}
			sample.bytes += int64(read)
		}
		if err != nil {
			readErr = err
			break
		}
	}
	windowElapsed := !timer.Stop()
	if windowElapsed {
		<-expired
	}
	sample.transfer = time.Since(firstByteAt)
	if windowElapsed {
		sample.transfer = time.Since(sampleStarted)
	}
	if sample.transfer > 0 && (windowElapsed || sample.bytes >= probeSampleMinimum) {
		sample.measurable = true
		sample.kbps = probeKbps(sample.bytes, sample.transfer)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) && !windowElapsed && sample.bytes < probeSampleBytes {
		return sample, readErr
	}
	return sample, nil
}

func probeKbps(bytes int64, transfer time.Duration) int64 {
	return bytes * 8 * int64(time.Second) / int64(transfer) / 1000
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
