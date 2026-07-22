//go:build lite

package pull

import (
	"context"
	"net/http"
)

type requestTrace struct{}

func startRequestTrace(ctx context.Context, _, _ string) (context.Context, requestTrace) {
	return ctx, requestTrace{}
}

func (requestTrace) finish(int, error) {}

func injectRequestTrace(context.Context, http.Header) {}
