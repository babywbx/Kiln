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
	if err := security.MediaHostOK(req.URL, c.allowed); err != nil {
		return Result{}, apperr.Wrap(apperr.CodeForbidden, 403, "upstream host not allowed", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
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
		StatusCode:  resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"),
		FinalURL:    resp.Request.URL.String(),
		ProxyID:     proxyID,
		ProxyReason: reason,
	}, nil
}

func (c *Client) GetBytes(ctx context.Context, req Request) ([]byte, string, error) {
	res, err := c.Get(ctx, req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	limited := io.LimitReader(res.Body, c.maxPlaylist+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return nil, "", apperr.Wrap(apperr.CodeUpstream, 502, "read upstream body failed", err)
	}
	if int64(len(b)) > c.maxPlaylist {
		return nil, "", apperr.New(apperr.CodeUpstream, 502, "upstream playlist too large")
	}
	return b, res.FinalURL, nil
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
