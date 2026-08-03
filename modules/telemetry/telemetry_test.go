package telemetry

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestSetupExportsOTLPHTTPSpans(t *testing.T) {
	var requests atomic.Int64
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("OTLP request path=%q content-type=%q", r.URL.Path, r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("empty OTLP request body")
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(collector.Close)
	t.Cleanup(func() { otel.SetTracerProvider(noop.NewTracerProvider()) })

	shutdown, err := Setup(t.Context(), config.Observe{
		Enabled: config.Bool(true), OTLPEndpoint: collector.URL, OTLPInsecure: true,
		TraceSampleRatio: 1, ServiceName: "kiln-test",
	}, "test-version")
	if err != nil {
		t.Fatal(err)
	}
	_, span := otel.Tracer("test").Start(context.Background(), "operation")
	span.End()
	if err := shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() == 0 {
		t.Fatal("no OTLP request received")
	}
}
