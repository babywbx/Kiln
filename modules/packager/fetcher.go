package packager

import (
	"context"
	"errors"

	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/version"
)

// PullFetcher reaches upstream through the existing pull client, so the native
// path inherits SSRF checks, the per-channel proxy route and the redirect
// policy without knowing any of them exist.
type PullFetcher struct {
	Client    *pull.Client
	ChannelID string
	UserAgent string
	Headers   map[string]string
	MaxBytes  int64
}

func (f *PullFetcher) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	if f.Client == nil {
		return nil, "", errors.New("packager: no upstream client")
	}
	return f.Client.GetBytesLimit(ctx, pull.Request{
		URL:       url,
		UserAgent: version.UserAgent(f.UserAgent),
		Headers:   f.Headers,
		ChannelID: f.ChannelID,
	}, f.MaxBytes)
}

// NewPullFetcher builds the per-request fetcher the native adapter needs.
func NewPullFetcher(client *pull.Client, maxBytes int64) func(req Request) Fetcher {
	return func(req Request) Fetcher {
		return &PullFetcher{
			Client:    client,
			ChannelID: req.ChannelID,
			UserAgent: req.UserAgent,
			Headers:   req.Headers,
			MaxBytes:  maxBytes,
		}
	}
}
