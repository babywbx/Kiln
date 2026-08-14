//go:build !lite

package pull

import (
	"context"
	"errors"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type requestTrace struct {
	span trace.Span
}

func startRequestTrace(ctx context.Context, method, host string) (context.Context, requestTrace) {
	ctx, span := otel.Tracer("kiln/pull").Start(ctx, "upstream.request")
	span.SetAttributes(attribute.String("http.request.method", method), attribute.String("server.address", host))
	return ctx, requestTrace{span: span}
}

func (t requestTrace) finish(status int, err error) {
	if err != nil {
		t.span.RecordError(errors.New("upstream request failed"))
		t.span.SetStatus(codes.Error, "upstream request failed")
	} else {
		t.span.SetAttributes(attribute.Int("http.response.status_code", status))
	}
	t.span.End()
}

func injectRequestTrace(ctx context.Context, header http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(header))
}
