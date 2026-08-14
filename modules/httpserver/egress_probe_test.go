package httpserver

import (
	"io"
	"strings"
	"testing"
	"time"
)

type trickleReader struct {
	remaining int
	chunk     int
	delay     time.Duration
}

func (t *trickleReader) Read(p []byte) (int, error) {
	if t.remaining <= 0 {
		return 0, io.EOF
	}
	time.Sleep(t.delay)
	size := min(min(t.chunk, len(p)), t.remaining)
	t.remaining -= size
	return size, nil
}

func TestSampleProbeBodyMeasuresATrickle(t *testing.T) {
	sample := sampleProbeBody(&trickleReader{remaining: 8 << 20, chunk: 32 << 10, delay: 20 * time.Millisecond})
	if !sample.measurable {
		t.Fatalf("sample of %d bytes was not measurable", sample.bytes)
	}
	if sample.kbps <= 0 || sample.kbps > 40_000 {
		t.Fatalf("throughput = %d kbps, want a slow but positive rate", sample.kbps)
	}
	if sample.bytes > probeSampleBytes {
		t.Fatalf("read %d bytes, past the %d budget", sample.bytes, probeSampleBytes)
	}
}

func TestSampleProbeBodyRefusesToJudgeASmallResponse(t *testing.T) {
	sample := sampleProbeBody(strings.NewReader(strings.Repeat("a", 4096)))
	if sample.measurable {
		t.Fatal("a 4 KB page cannot say whether the path carries media")
	}
	if sample.bytes != 4096 {
		t.Fatalf("bytes = %d, want the whole body", sample.bytes)
	}
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
