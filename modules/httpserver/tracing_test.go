package httpserver_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/observe"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestServerTracesOnlyWhenTelemetryIsReady(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	otel.SetTracerProvider(provider)

	serveHealth := func(cfg config.File, tracing bool) {
		server := httpserver.New(httpserver.Deps{
			Cfg:     cfg,
			Observe: observe.New(),
			Log:     benchmarkLogger(),
			Tracing: tracing,
		})
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("health status = %d", response.Code)
		}
	}

	configured := config.File{Observe: config.Observe{
		Enabled:      true,
		OTLPEndpoint: "http://collector.test/v1/traces",
	}}
	serveHealth(configured, false)
	if spans := recorder.Ended(); len(spans) != 0 {
		t.Fatalf("spans before telemetry is ready = %d, want 0", len(spans))
	}

	serveHealth(configured, true)
	if spans := recorder.Ended(); len(spans) != 1 {
		t.Fatalf("spans with exporter = %d, want 1", len(spans))
	}
}
