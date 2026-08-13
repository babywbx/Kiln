package pull

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/security"
)

type Client struct {
	router      *proxyegress.Router
	observe     *observe.Service
	allowed     map[string]struct{}
	maxPlaylist int64
	stall       time.Duration
	pinned      http.RoundTripper
}

type Options struct {
	Observe      *observe.Service
	Allowed      map[string]struct{}
	MaxPlaylist  int64
	ProxyURL     string
	Router       *proxyegress.Router
	StallTimeout time.Duration
}

func New(opt Options) *Client {
	if opt.StallTimeout <= 0 {
		opt.StallTimeout = 30 * time.Second
	}
	if opt.MaxPlaylist <= 0 {
		opt.MaxPlaylist = 8 << 20
	}
	tr := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: proxyegress.UpstreamTLSConfig(),
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
		router:      opt.Router,
		observe:     opt.Observe,
		allowed:     opt.Allowed,
		maxPlaylist: opt.MaxPlaylist,
		stall:       opt.StallTimeout,
		pinned:      proxyegress.NewPinnedTransport(tr, opt.Router, "", opt.Allowed),
	}
}

type Result struct {
	Body          io.ReadCloser
	Header        http.Header
	StatusCode    int
	ContentLength int64
	ContentType   string
	FinalURL      string
	ProxyID       string
	ProxyReason   string
}

type Request struct {
	URL                      string
	UserAgent                string
	Headers                  map[string]string
	ForwardHeaders           http.Header
	HeaderOrigin             string
	ChannelID                string
	StopRedirect             bool
	UpgradeInsecureRedirects bool
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
	ctx, abandon := context.WithCancel(ctx)
	defer func() {
		if resultErr != nil {
			abandon()
		}
	}()
	if err := security.MediaHostOK(req.URL, c.allowed); err != nil {
		return Result{}, apperr.Wrap(apperr.CodeForbidden, 403, "upstream host not allowed", err)
	}
	if method != http.MethodGet && method != http.MethodHead {
		return Result{}, apperr.New(apperr.CodeInvalid, 400, "unsupported upstream method")
	}
	httpReq, err := http.NewRequestWithContext(proxyegress.WithChannelID(ctx, req.ChannelID), method, req.URL, nil)
	if err != nil {
		return Result{}, apperr.Wrap(apperr.CodeInvalid, 400, "bad upstream url", err)
	}
	if req.UserAgent != "" {
		httpReq.Header.Set("User-Agent", req.UserAgent)
	}
	for name, values := range req.ForwardHeaders {
		for _, value := range values {
			if name != "" && value != "" {
				httpReq.Header.Add(name, value)
			}
		}
	}
	headerOrigin := req.HeaderOrigin
	if headerOrigin != "" && sameOrigin(req.URL, headerOrigin) {
		for k, v := range req.Headers {
			if k != "" && v != "" {
				httpReq.Header.Set(k, v)
			}
		}
	}
	injectRequestTrace(ctx, httpReq.Header)

	proxyID := proxyegress.Direct
	reason := "fallback"
	if c.router != nil {
		d := c.router.Resolve(req.URL, req.ChannelID)
		proxyID, reason = d.ProxyID, d.Reason
	}
	client := c.pinnedClient(headerOrigin, req.Headers, req.StopRedirect, req.UpgradeInsecureRedirects)

	resp, err := client.Do(httpReq)
	if err != nil {
		return Result{}, apperr.Wrap(apperr.CodeUpstream, 502, "upstream request failed", err)
	}
	resp.Body = newStallGuard(resp.Body, c.observe, c.stall, abandon)
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Result{}, apperr.New(apperr.CodeUpstream, 502, fmt.Sprintf("upstream status %s: %s", resp.Status, string(b)))
	}
	return Result{
		Body:          resp.Body,
		Header:        resp.Header.Clone(),
		StatusCode:    resp.StatusCode,
		ContentLength: resp.ContentLength,
		ContentType:   resp.Header.Get("Content-Type"),
		FinalURL:      resp.Request.URL.String(),
		ProxyID:       proxyID,
		ProxyReason:   reason,
	}, nil
}

func (c *Client) GetBytes(ctx context.Context, req Request) ([]byte, string, error) {
	return c.GetBytesLimit(ctx, req, c.maxPlaylist)
}

func (c *Client) GetBytesReserve(ctx context.Context, req Request, reserve func(int64) error) ([]byte, string, error) {
	return c.GetBytesLimitReserve(ctx, req, c.maxPlaylist, reserve)
}

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
	knownLength := res.ContentLength
	if knownLength >= 0 {
		maxInt := int64(^uint(0) >> 1)
		if knownLength > max || knownLength > maxInt {
			return nil, "", apperr.New(apperr.CodeUpstream, 502, "upstream response too large")
		}
		if reserve != nil && knownLength > 0 {
			if err := reserve(knownLength); err != nil {
				return nil, "", err
			}
		}
		b = make([]byte, 0, int(knownLength))
	}
	for {
		if knownLength >= 0 && int64(len(b)) == knownLength {
			done, readErr := confirmBodyEnd(res.Body, "upstream response exceeds content length")
			if readErr != nil {
				return nil, "", readErr
			}
			if done {
				return b, res.FinalURL, nil
			}
			continue
		}
		if int64(len(b)) == max {
			done, readErr := confirmBodyEnd(res.Body, "upstream response too large")
			if readErr != nil {
				return nil, "", readErr
			}
			if done {
				return b, res.FinalURL, nil
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

func confirmBodyEnd(body io.Reader, overflowMessage string) (bool, error) {
	var extra [1]byte
	n, err := body.Read(extra[:])
	if n > 0 {
		return false, apperr.New(apperr.CodeUpstream, 502, overflowMessage)
	}
	if err == io.EOF {
		return true, nil
	}
	if err != nil {
		return false, apperr.Wrap(apperr.CodeUpstream, 502, "read upstream body failed", err)
	}
	return false, nil
}

func (c *Client) Router() *proxyegress.Router { return c.router }

func (c *Client) ExplicitlyAllowsURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	_, ok := c.allowed[host]
	return host != "" && ok
}

func (c *Client) PinURL(ctx context.Context, rawURL string) (*url.URL, error) {
	return security.PinPublicProbeURL(ctx, rawURL, c.allowed)
}

func (c *Client) UpgradeInsecureURL(u *url.URL) bool {
	return upgradeInsecureURL(u, c.allowed)
}

func (c *Client) pinnedClient(
	headerOrigin string,
	customHeaders map[string]string,
	stopRedirect bool,
	upgradeInsecure bool,
) *http.Client {
	return &http.Client{
		Transport: c.pinned,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			upgraded := upgradeInsecure && c.UpgradeInsecureURL(req.URL)
			if stopRedirect {
				if upgraded && req.Response != nil {
					if req.Response.Header == nil {
						req.Response.Header = make(http.Header)
					}
					req.Response.Header.Set("Location", req.URL.String())
				}
				return http.ErrUseLastResponse
			}
			if req.Response != nil {
				_ = req.Response.Body.Close()
				req.Response.Body = http.NoBody
			}
			if len(via) >= 8 {
				return fmt.Errorf("too many redirects")
			}
			if !sameOrigin(req.URL.String(), headerOrigin) {
				for name := range customHeaders {
					req.Header.Del(name)
				}
				req.Header.Del("Referer")
			}
			return nil
		},
	}
}

func upgradeInsecureURL(u *url.URL, allowedPrivate map[string]struct{}) bool {
	if u == nil || u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" || isLocalOrSpecialHost(host) {
		return false
	}
	if _, explicitlyAllowed := allowedPrivate[host]; explicitlyAllowed {
		return false
	}
	if port := u.Port(); port != "" && port != "80" {
		return false
	}
	u.Scheme = "https"
	u.Host = host
	if strings.Contains(u.Host, ":") {
		u.Host = "[" + u.Host + "]"
	}
	return true
}

func isLocalOrSpecialHost(host string) bool {
	if strings.Contains(host, "%") || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return !security.IsPublicIP(ip)
}

func sameOrigin(left, right string) bool {
	a, err := url.Parse(left)
	if err != nil || a.Hostname() == "" {
		return false
	}
	b, err := url.Parse(right)
	if err != nil || b.Hostname() == "" {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(strings.TrimSuffix(a.Hostname(), "."), strings.TrimSuffix(b.Hostname(), ".")) &&
		originPort(a) == originPort(b)
}

func originPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return ""
}

type stallGuard struct {
	rc      io.ReadCloser
	obs     *observe.Service
	stall   time.Duration
	watch   *time.Timer
	abandon context.CancelFunc
}

func newStallGuard(
	rc io.ReadCloser, obs *observe.Service, stall time.Duration, abandon context.CancelFunc,
) *stallGuard {
	return &stallGuard{rc: rc, obs: obs, stall: stall, watch: time.AfterFunc(stall, abandon), abandon: abandon}
}

func (s *stallGuard) Read(p []byte) (int, error) {
	n, err := s.rc.Read(p)
	if n > 0 {
		if s.obs != nil {
			s.obs.AddBytesIn(int64(n))
		}
		s.watch.Reset(s.stall)
	}
	return n, err
}

func (s *stallGuard) Close() error {
	s.watch.Stop()
	s.abandon()
	return s.rc.Close()
}
