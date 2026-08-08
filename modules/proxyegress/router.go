package proxyegress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

const Direct = "direct"

type PlaylistPolicy string

const (
	PolicyRewrite     PlaylistPolicy = "rewrite"
	PolicyPassthrough PlaylistPolicy = "passthrough"
	PolicyAuto        PlaylistPolicy = "auto"
)

type Profile struct {
	ID       string
	Name     string
	URL      string
	Disabled bool
}

type RuleKind string

const (
	KindHostSuffix RuleKind = "host_suffix"
	KindHostExact  RuleKind = "host_exact"
	KindHostRegex  RuleKind = "host_regex"
	KindChannel    RuleKind = "channel_id"
	KindURLRegex   RuleKind = "url_regex"
)

type Rule struct {
	ID       string
	Priority int
	Kind     RuleKind
	Pattern  string
	ProxyID  string
	Disabled bool
}

type Config struct {
	Default         string
	PlaylistPolicy  PlaylistPolicy
	Profiles        []Profile
	Rules           []Rule
	DockerProxyHost string
}

type Decision struct {
	ProxyID  string
	ProxyURL *url.URL
	Rewrite  bool
	Reason   string
}

type Router struct {
	mu       sync.RWMutex
	cfg      Config
	profiles map[string]*url.URL
	rules    []Rule
	clients  map[string]*http.Client
}

func NewRouter(cfg Config) (*Router, error) {
	r := &Router{
		profiles: map[string]*url.URL{},
		clients:  map[string]*http.Client{},
	}
	if err := r.Reload(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Router) Reload(cfg Config) error {
	if cfg.Default == "" {
		cfg.Default = Direct
	}
	if cfg.PlaylistPolicy == "" {
		cfg.PlaylistPolicy = PolicyRewrite
	}
	if cfg.DockerProxyHost == "" {
		cfg.DockerProxyHost = "host.docker.internal"
	}
	switch cfg.PlaylistPolicy {
	case PolicyRewrite, PolicyPassthrough, PolicyAuto:
	default:
		return fmt.Errorf("invalid playlist policy %q", cfg.PlaylistPolicy)
	}
	profs := map[string]*url.URL{}
	for _, p := range cfg.Profiles {
		if p.Disabled {
			continue
		}
		if p.ID == "" || p.URL == "" {
			return fmt.Errorf("proxy requires id and url")
		}
		u, err := url.Parse(p.URL)
		if err != nil {
			return fmt.Errorf("proxy %q url: %w", p.ID, err)
		}
		if u.Host == "" {
			return fmt.Errorf("proxy %q url requires a host", p.ID)
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("proxy %q unsupported scheme %q", p.ID, u.Scheme)
		}
		profs[p.ID] = u
	}
	if cfg.Default != Direct {
		if _, ok := profs[cfg.Default]; !ok {
			return fmt.Errorf("egress.default references unknown proxy %q", cfg.Default)
		}
	}
	rules := append([]Rule(nil), cfg.Rules...)
	for i := range rules {
		rule := &rules[i]
		if rule.Disabled {
			continue
		}
		if rule.Kind == "" {
			rule.Kind = KindHostSuffix
		}
		if rule.Pattern == "" {
			return fmt.Errorf("egress rule %q requires a pattern", rule.ID)
		}
		if rule.ProxyID == "" {
			return fmt.Errorf("egress rule %q requires a proxy", rule.ID)
		}
		if rule.ProxyID != Direct {
			if _, ok := profs[rule.ProxyID]; !ok {
				return fmt.Errorf("egress rule %q references unknown or disabled proxy %q", rule.ID, rule.ProxyID)
			}
		}
		switch rule.Kind {
		case KindHostSuffix, KindHostExact, KindChannel:
		case KindHostRegex, KindURLRegex:
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return fmt.Errorf("egress rule %q pattern: %w", rule.ID, err)
			}
		default:
			return fmt.Errorf("egress rule %q has invalid kind %q", rule.ID, rule.Kind)
		}
	}
	for i := 0; i < len(rules); i++ {
		for j := i + 1; j < len(rules); j++ {
			if rules[j].Priority < rules[i].Priority {
				rules[i], rules[j] = rules[j], rules[i]
			}
		}
	}
	r.mu.Lock()
	r.cfg = cfg
	r.profiles = profs
	r.rules = rules
	r.clients = map[string]*http.Client{}
	r.mu.Unlock()
	return nil
}

func (r *Router) Config() Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cfg
}

func (r *Router) Resolve(targetURL, channelID string) Decision {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.resolveLocked(targetURL, channelID)
}

func (r *Router) resolveLocked(targetURL, channelID string) Decision {
	u, err := url.Parse(targetURL)
	if err != nil || u.Host == "" {
		return Decision{ProxyID: Direct, Reason: "invalid-url", Rewrite: r.cfg.PlaylistPolicy == PolicyRewrite}
	}
	host := strings.ToLower(u.Hostname())
	proxyID := r.cfg.Default
	reason := "default"

	for _, rule := range r.rules {
		if rule.Disabled {
			continue
		}
		if rule.Pattern == "" && rule.Kind != KindChannel {
			continue
		}
		match := false
		kind := rule.Kind
		if kind == "" {
			kind = KindHostSuffix
		}
		switch kind {
		case KindHostExact:
			match = host == strings.ToLower(rule.Pattern)
		case KindHostSuffix:
			match = matchHostSuffix(host, rule.Pattern)
		case KindHostRegex:
			if re, err := regexp.Compile(rule.Pattern); err == nil {
				match = re.MatchString(host)
			}
		case KindChannel:
			match = channelID != "" && channelID == rule.Pattern
		case KindURLRegex:
			if re, err := regexp.Compile(rule.Pattern); err == nil {
				match = re.MatchString(targetURL)
			}
		}
		if !match {
			continue
		}
		proxyID = rule.ProxyID
		if proxyID == "" {
			proxyID = Direct
		}
		reason = string(kind) + ":" + rule.Pattern
		break
	}

	d := Decision{ProxyID: proxyID, Reason: reason}
	if proxyID != Direct {
		if u, ok := r.profiles[proxyID]; ok {
			d.ProxyURL = u
		} else {
			d.ProxyID = Direct
			d.Reason = reason + "|missing-profile"
		}
	}
	switch r.cfg.PlaylistPolicy {
	case PolicyPassthrough:
		d.Rewrite = false
	case PolicyAuto:
		d.Rewrite = d.ProxyID != Direct
	default:
		d.Rewrite = true
	}
	return d
}

func (r *Router) ClientFor(d Decision, timeout time.Duration) (*http.Client, error) {
	return r.ClientForChannel(d.ProxyID, "", timeout)
}

func (r *Router) ClientForChannel(hintProxyID, channelID string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	_ = hintProxyID
	rt := &routingTransport{router: r, channelID: channelID}
	return &http.Client{Timeout: timeout, Transport: rt}, nil
}

func (r *Router) ClientForProxy(proxyID string, timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if proxyID == "" {
		proxyID = Direct
	}
	decision := Decision{ProxyID: proxyID}
	if proxyID != Direct {
		r.mu.RLock()
		proxyURL, ok := r.profiles[proxyID]
		r.mu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown proxy %q", proxyID)
		}
		decision.ProxyURL = proxyURL
	}
	fixed, err := r.fixedClient(decision)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: timeout, Transport: fixed.Transport}, nil
}

type routingTransport struct {
	router    *Router
	channelID string
}

func (t *routingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d := t.router.Resolve(req.URL.String(), t.channelID)
	c, err := t.router.fixedClient(d)
	if err != nil {
		return nil, err
	}
	return c.Transport.RoundTrip(req)
}

func (r *Router) fixedClient(d Decision) (*http.Client, error) {
	key := d.ProxyID
	if key == "" {
		key = Direct
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[key]; ok {
		return c, nil
	}
	c, err := buildClient(d.ProxyURL, 0)
	if err != nil {
		return nil, err
	}
	r.clients[key] = c
	return c, nil
}

func buildClient(proxyURL *url.URL, _ time.Duration) (*http.Client, error) {
	tr := &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   12 * time.Second,
		ResponseHeaderTimeout: 25 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if proxyURL != nil {
		compatTLSForProxy(tr)
		scheme := strings.ToLower(proxyURL.Scheme)
		switch scheme {
		case "http", "https":
			tr.Proxy = http.ProxyURL(proxyURL)
		case "socks5", "socks5h":
			u := *proxyURL
			if scheme == "socks5h" {
				u.Scheme = "socks5"
			}
			d, err := proxy.FromURL(&u, proxy.Direct)
			if err != nil {
				return nil, err
			}
			if cd, ok := d.(proxy.ContextDialer); ok {
				tr.DialContext = cd.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return d.Dial(network, addr)
				}
			}
			tr.Proxy = nil
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %s", scheme)
		}
	}
	return &http.Client{Transport: tr}, nil
}

func compatTLSForProxy(tr *http.Transport) {
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(authority string, c *tls.Conn) http.RoundTripper{}
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
}

func (r *Router) EnvForFFmpeg(targetURL, channelID string, forDocker bool) ([]string, error) {
	r.mu.RLock()
	d := r.resolveLocked(targetURL, channelID)
	dockerProxyHost := r.cfg.DockerProxyHost
	r.mu.RUnlock()
	if d.ProxyID == Direct || d.ProxyURL == nil {
		return nil, nil
	}
	if s := strings.ToLower(d.ProxyURL.Scheme); s != "http" && s != "https" {
		return nil, fmt.Errorf("proxy %q uses %s, which ffmpeg cannot use; route %s through an http proxy or direct",
			d.ProxyID, s, targetURL)
	}
	u := *d.ProxyURL
	if forDocker {
		u = rewriteProxyHostForDocker(u, dockerProxyHost)
	}
	proxyStr := u.String()
	return []string{
		"HTTP_PROXY=" + proxyStr,
		"HTTPS_PROXY=" + proxyStr,
		"http_proxy=" + proxyStr,
		"https_proxy=" + proxyStr,
		"NO_PROXY=localhost,127.0.0.1",
		"no_proxy=localhost,127.0.0.1",
	}, nil
}

func matchHostSuffix(host, pattern string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	pattern = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(pattern, ".")))
	if host == "" || pattern == "" {
		return false
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

func rewriteProxyHostForDocker(u url.URL, dockerHost string) url.URL {
	h := strings.ToLower(u.Hostname())
	if h == "127.0.0.1" || h == "localhost" || h == "::1" {
		port := u.Port()
		if port == "" {
			u.Host = dockerHost
		} else {
			u.Host = dockerHost + ":" + port
		}
	}
	return u
}

func (r *Router) ShouldRewriteURL(abs, channelID string) bool {
	return r.Resolve(abs, channelID).Rewrite
}
