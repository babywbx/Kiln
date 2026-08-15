package httpserver

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type trickleReader struct {
	remaining int
	chunk     int
	delay     time.Duration
	closed    chan struct{}
	closeOnce sync.Once
}

func (t *trickleReader) Read(p []byte) (int, error) {
	if t.remaining <= 0 {
		return 0, io.EOF
	}
	select {
	case <-time.After(t.delay):
	case <-t.closed:
		return 0, io.ErrClosedPipe
	}
	size := min(min(t.chunk, len(p)), t.remaining)
	t.remaining -= size
	return size, nil
}

func (t *trickleReader) Close() error {
	t.closeOnce.Do(func() { close(t.closed) })
	return nil
}

func TestSampleProbeBodyMeasuresSlowWindow(t *testing.T) {
	reader := &trickleReader{
		remaining: 8 << 20,
		chunk:     1 << 10,
		delay:     32 * time.Millisecond,
		closed:    make(chan struct{}),
	}
	started := time.Now()
	sample, err := sampleProbeBodyWithin(reader, time.Now(), 350*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !sample.measurable {
		t.Fatalf("sample of %d bytes was not measurable", sample.bytes)
	}
	// Coarse timers stretch the trickle, so the band only pins the magnitude.
	if sample.kbps < 100 || sample.kbps > 320 {
		t.Fatalf("throughput = %d kbps, want 100-320", sample.kbps)
	}
	if sample.kbps >= probeThroughputFloor(0) {
		t.Fatalf("throughput = %d kbps, want the default verdict to be slow", sample.kbps)
	}
	if sample.bytes >= probeSampleMinimum {
		t.Fatalf("read %d bytes, want less than the old minimum", sample.bytes)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("sampling took %s, want a hard cutoff", elapsed)
	}
}

func TestSampleProbeBodyRefusesToJudgeASmallResponse(t *testing.T) {
	requestStarted := time.Now().Add(-time.Second)
	sample, err := sampleProbeBody(io.NopCloser(strings.NewReader(strings.Repeat("a", 4096))), requestStarted)
	if err != nil {
		t.Fatal(err)
	}
	if sample.measurable {
		t.Fatal("a 4 KB page cannot say whether the path carries media")
	}
	if sample.bytes != 4096 {
		t.Fatalf("bytes = %d, want the whole body", sample.bytes)
	}
	if sample.firstByte < 900*time.Millisecond {
		t.Fatalf("ttfb = %s, want it measured from the request start", sample.firstByte)
	}
}

func TestSampleProbeBodyCountsLateFirstByteInTheWindow(t *testing.T) {
	reader := &trickleReader{
		remaining: 8 << 20,
		chunk:     8 << 10,
		delay:     320 * time.Millisecond,
		closed:    make(chan struct{}),
	}
	sample, err := sampleProbeBodyWithin(reader, time.Now(), 350*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !sample.measurable || sample.kbps <= 0 || sample.kbps > 300 {
		t.Fatalf("sample = %+v, want the late first chunk counted across the full window", sample)
	}
}

func TestProbeKbpsHandlesSubMillisecondTransfer(t *testing.T) {
	if got := probeKbps(probeSampleBytes, 500*time.Microsecond); got <= 0 {
		t.Fatalf("throughput = %d kbps, want a positive rate", got)
	}
}

func TestSampleProbeBodyReportsInterruptedResponse(t *testing.T) {
	body := io.NopCloser(io.MultiReader(strings.NewReader("partial"), probeErrorReader{}))
	if _, err := sampleProbeBody(body, time.Now()); err == nil {
		t.Fatal("interrupted response was accepted")
	}
}

type probeErrorReader struct{}

func (probeErrorReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestProbeThroughputFloorFollowsTheConfiguredValue(t *testing.T) {
	if got := probeThroughputFloor(0); got != probeDefaultFloor {
		t.Fatalf("zero = %d, want the default %d", got, probeDefaultFloor)
	}
	if got := probeThroughputFloor(-1); got != 0 {
		t.Fatalf("negative = %d, want the verdict disabled", got)
	}
	if got := probeThroughputFloor(2500); got != 2500 {
		t.Fatalf("configured = %d, want 2500", got)
	}
}
