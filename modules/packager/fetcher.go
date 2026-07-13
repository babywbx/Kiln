package packager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

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

func (f *PullFetcher) FetchClock(ctx context.Context, url, method string) ([]byte, string, error) {
	if f.Client == nil {
		return nil, "", errors.New("packager: no upstream client")
	}
	res, err := f.Client.Do(ctx, method, pull.Request{
		URL: url, UserAgent: version.UserAgent(f.UserAgent), Headers: f.Headers, ChannelID: f.ChannelID,
	})
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil {
		return nil, "", err
	}
	if len(body) > 4096 {
		return nil, "", fmt.Errorf("clock response too large")
	}
	if method == http.MethodHead {
		body = nil
	}
	return body, res.Header.Get("Date"), nil
}

func (f *PullFetcher) Fetch(ctx context.Context, url string) ([]byte, string, error) {
	if f.Client == nil {
		return nil, "", errors.New("packager: no upstream client")
	}
	return f.Client.GetBytes(ctx, pull.Request{
		URL:       url,
		UserAgent: version.UserAgent(f.UserAgent),
		Headers:   f.Headers,
		ChannelID: f.ChannelID,
	})
}

func (f *PullFetcher) FetchManifestReserved(ctx context.Context, url string, reserve func(int64) error) ([]byte, string, error) {
	if f.Client == nil {
		return nil, "", errors.New("packager: no upstream client")
	}
	return f.Client.GetBytesReserve(ctx, pull.Request{
		URL:       url,
		UserAgent: version.UserAgent(f.UserAgent),
		Headers:   f.Headers,
		ChannelID: f.ChannelID,
	}, reserve)
}

func (f *PullFetcher) FetchReserved(ctx context.Context, url string, reserve func(int64) error) ([]byte, string, error) {
	if f.Client == nil {
		return nil, "", errors.New("packager: no upstream client")
	}
	return f.Client.GetBytesLimitReserve(ctx, pull.Request{
		URL:       url,
		UserAgent: version.UserAgent(f.UserAgent),
		Headers:   f.Headers,
		ChannelID: f.ChannelID,
	}, f.MaxBytes, reserve)
}

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
