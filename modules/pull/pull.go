package pull

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/security"
)

type Client struct {
	fallback    *http.Client
	router      *proxyegress.Router
	observe     *observe.Service
	allowed     map[string]struct{}
	maxPlaylist int64
	timeout     time.Duration
}

type Options struct {
	Observe     *observe.Service
	Allowed     map[string]struct{}
	MaxPlaylist int64
	ProxyURL    string
	Router      *proxyegress.Router
	Timeout     time.Duration
}

func New(opt Options) *Client {
	if opt.Timeout <= 0 {
		opt.Timeout = 30 * time.Second
	}
	if opt.MaxPlaylist <= 0 {
		opt.MaxPlaylist = 8 << 20
	}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
	}
	if opt.ProxyURL != "" && opt.Router == nil {
		if u, err := url.Parse(opt.ProxyURL); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	return &Client{
		fallback: &http.Client{
			Timeout:   opt.Timeout,
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 8 {
					return fmt.Errorf("too many redirects")
				}
				if err := security.HostAllowed(req.URL.String(), opt.Allowed); err != nil {
					if len(opt.Allowed) > 0 {
						if err2 := security.MediaHostOK(req.URL.String(), opt.Allowed); err2 != nil {
							return err2
						}
					}
				}
				return nil
			},
		},
		router:      opt.Router,
		observe:     opt.Observe,
		allowed:     opt.Allowed,
		maxPlaylist: opt.MaxPlaylist,
		timeout:     opt.Timeout,
	}
}

type Result struct {
	Body        io.ReadCloser
	Header      http.Header
	StatusCode  int
	ContentType string
	FinalURL    string
	ProxyID     string
	ProxyReason string
}

type Request struct {
	URL       string
	UserAgent string
	Headers   map[string]string
	ChannelID string
}

func (c *Client) Get(ctx context.Context, req Request) (Result, error) {
	return c.Do(ctx, http.MethodGet, req)
}

func (c *Client) Do(ctx context.Context, method string, req Request) (result Result, resultErr error) {
	host := ""
	if parsed, err := url.Parse(req.URL); err == nil {
		host = parsed.Hostname()
	}
	ctx, trace := startRequestTrace(ctx, method, host)
	defer func() {
		trace.finish(result.StatusCode, resultErr)
	}()
	if err := security.MediaHostOK(req.URL, c.allowed); err != nil {
		return Result{}, apperr.Wrap(apperr.CodeForbidden, 403, "upstream host not allowed", err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		return Result{}, apperr.New(apperr.CodeInvalid, 400, "unsupported upstream method")
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, req.URL, nil)
	if err != nil {
		return Result{}, apperr.Wrap(apperr.CodeInvalid, 400, "bad upstream url", err)
	}
	if req.UserAgent != "" {
		httpReq.Header.Set("User-Agent", req.UserAgent)
	}
	for k, v := range req.Headers {
		if k != "" && v != "" {
			httpReq.Header.Set(k, v)
		}
	}
	injectRequestTrace(ctx, httpReq.Header)

	client := c.fallback
	proxyID := proxyegress.Direct
	reason := "fallback"
	if c.router != nil {
		d := c.router.Resolve(req.URL, req.ChannelID)
		proxyID, reason = d.ProxyID, d.Reason
		hc, err := c.router.ClientForChannel(d.ProxyID, req.ChannelID, c.timeout)
		if err != nil {
			return Result{}, apperr.Wrap(apperr.CodeUpstream, 502, "proxy client failed", err)
		}
		hc2 := *hc
		hc2.CheckRedirect = func(r *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			if err := security.MediaHostOK(r.URL.String(), c.allowed); err != nil {
				return err
			}
			return nil
		}
		client = &hc2
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, apperr.Wrap(apperr.CodeUpstream, 502, "upstream request failed", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Result{}, apperr.New(apperr.CodeUpstream, 502, fmt.Sprintf("upstream status %s: %s", resp.Status, string(b)))
	}
	body := &countingReadCloser{rc: resp.Body, obs: c.observe}
	return Result{
		Body:        body,
		Header:      resp.Header.Clone(),
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		FinalURL:    resp.Request.URL.String(),
		ProxyID:     proxyID,
		ProxyReason: reason,
	}, nil
}

func (c *Client) GetBytes(ctx context.Context, req Request) ([]byte, string, error) {
	return c.GetBytesLimit(ctx, req, c.maxPlaylist)
}

func (c *Client) GetBytesReserve(ctx context.Context, req Request, reserve func(int64) error) ([]byte, string, error) {
	return c.GetBytesLimitReserve(ctx, req, c.maxPlaylist, reserve)
}

// GetBytesLimit reads at most max bytes. Media segments are far larger than
// playlists, so they need their own ceiling rather than the playlist one.
func (c *Client) GetBytesLimit(ctx context.Context, req Request, max int64) ([]byte, string, error) {
	return c.GetBytesLimitReserve(ctx, req, max, nil)
}

func (c *Client) GetBytesLimitReserve(ctx context.Context, req Request, max int64, reserve func(int64) error) ([]byte, string, error) {
	if max <= 0 {
		max = c.maxPlaylist
	}
	res, err := c.Get(ctx, req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	var b []byte
	for {
		if int64(len(b)) == max {
			var extra [1]byte
			n, readErr := res.Body.Read(extra[:])
			if n > 0 {
				return nil, "", apperr.New(apperr.CodeUpstream, 502, "upstream response too large")
			}
			if readErr == io.EOF {
				return b, res.FinalURL, nil
			}
			if readErr != nil {
				return nil, "", apperr.Wrap(apperr.CodeUpstream, 502, "read upstream body failed", readErr)
			}
			continue
		}
		if len(b) == cap(b) {
			oldCapacity := int64(cap(b))
			next := oldCapacity * 2
			if next < 32<<10 {
				next = 32 << 10
			}
			if next > max {
				next = max
			}
			if next <= oldCapacity {
				return nil, "", apperr.New(apperr.CodeUpstream, 502, "upstream response too large")
			}
			if reserve != nil {
				if next > int64(^uint64(0)>>1)-oldCapacity {
					return nil, "", apperr.New(apperr.CodeUpstream, 502, "upstream response too large")
				}
				if err := reserve(oldCapacity + next); err != nil {
					return nil, "", err
				}
			}
			grown := make([]byte, len(b), next)
			copy(grown, b)
			b = grown
			if reserve != nil {
				if err := reserve(next); err != nil {
					return nil, "", err
				}
			}
		}
		n, readErr := res.Body.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if int64(len(b)) > max {
			return nil, "", apperr.New(apperr.CodeUpstream, 502, "upstream response too large")
		}
		if readErr == io.EOF {
			return b, res.FinalURL, nil
		}
		if readErr != nil {
			return nil, "", apperr.Wrap(apperr.CodeUpstream, 502, "read upstream body failed", readErr)
		}
	}
}

func (c *Client) Router() *proxyegress.Router { return c.router }

type countingReadCloser struct {
	rc  io.ReadCloser
	obs *observe.Service
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 && c.obs != nil {
		c.obs.AddBytesIn(int64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }
