//go:build !lite

package pull

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTransportErrorsDoNotExposeUpstreamQueryInTrace(t *testing.T) {
	previousProvider := otel.GetTracerProvider()
	t.Cleanup(func() { otel.SetTracerProvider(previousProvider) })
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	t.Cleanup(func() { _ = provider.Shutdown(t.Context()) })
	otel.SetTracerProvider(provider)

	origin := httptest.NewServer(http.NotFoundHandler())
	target := origin.URL + "/segment.ts?token=upstream-secret&sig=upstream-signature"
	origin.Close()

	client := New(Options{Allowed: map[string]struct{}{"127.0.0.1": {}}})
	if _, err := client.Get(t.Context(), Request{URL: target}); err == nil {
		t.Fatal("expected transport error")
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if span.Status().Code != codes.Error || span.Status().Description != "upstream request failed" {
		t.Fatalf("span status = %+v", span.Status())
	}
	message := ""
	for _, event := range span.Events() {
		for _, attribute := range event.Attributes {
			if attribute.Key == "exception.message" {
				message = attribute.Value.AsString()
			}
		}
	}
	if message != "upstream request failed" {
		t.Fatalf("trace error message = %q, want fixed message", message)
	}
}
