package observe

import (
	"strings"
	"testing"
	"time"
)

func TestPrometheusExpositionIncludesServiceAndPackagerMetrics(t *testing.T) {
	service := New()
	service.AddBytesIn(10)
	service.AddBytesOut(20)
	service.IncRequest()
	service.IncError()
	service.UpsertSession(SessionStat{
		ChannelID: "news", Engine: "native_rewrite", State: "running", StartedAt: time.Now(),
		Packager: &PackagerStat{SegmentsPublished: 3, ManifestErrs: 1, ClockOffsetSeconds: 0.25},
	})
	var output strings.Builder
	if err := service.WritePrometheus(&output); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{
		"kiln_bytes_in_total 10",
		"kiln_bytes_out_total 20",
		"kiln_http_requests_total 1",
		"kiln_errors_total 1",
		"kiln_sessions 1",
		`kiln_session_info{channel="news",engine="native_rewrite",state="running"} 1`,
		`kiln_packager_segments_published_total{channel="news"} 3`,
		`kiln_packager_manifest_errors_total{channel="news"} 1`,
		`kiln_packager_clock_offset_seconds{channel="news"} 0.25`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("metrics missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(strings.ToLower(got), "token") || strings.Contains(got, "LastError") {
		t.Fatalf("metrics exposed sensitive or unbounded values:\n%s", got)
	}
}
