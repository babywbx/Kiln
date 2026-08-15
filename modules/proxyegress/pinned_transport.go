package proxyegress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/babywbx/kiln/modules/security"
)

func NewPinnedTransport(
	base *http.Transport,
	router *Router,
	channelID string,
	allowedPrivate map[string]struct{},
) http.RoundTripper {
	return newPinnedTransport(base, router, channelID, allowedPrivate, security.PinPublicProbeURL)
}

const (
	maxTransportRebuilds = 4
	maxPinnedTransports  = 256
)

var ErrUnpinnableProxyTarget = errors.New("plain HTTP target cannot be securely pinned through an HTTP proxy")

type channelIDContextKey struct{}

func WithChannelID(ctx context.Context, channelID string) context.Context {
	if channelID == "" {
		return ctx
	}
	return context.WithValue(ctx, channelIDContextKey{}, channelID)
}

type destinationPinner func(context.Context, string, map[string]struct{}) (*url.URL, error)

func newPinnedTransport(
	base *http.Transport,
	router *Router,
	channelID string,
	allowedPrivate map[string]struct{},
	pin destinationPinner,
) http.RoundTripper {
	return &pinnedTransport{
		base:           base,
		router:         router,
		channelID:      channelID,
		allowedPrivate: allowedPrivate,
		pin:            pin,
		transports:     map[pinnedTransportKey]pinnedTransportEntry{},
	}
}

type pinnedTransport struct {
	base           *http.Transport
	router         *Router
	channelID      string
	allowedPrivate map[string]struct{}
	pin            destinationPinner
	mu             sync.Mutex
	generation     uint64
	transports     map[pinnedTransportKey]pinnedTransportEntry
}

type pinnedTransportKey struct {
	base              *http.Transport
	scheme            string
	authority         string
	serverName        string
	proxy             string
	proxyViaTransport bool
}

type pinnedTransportEntry struct {
	pinnedHost string
	transport  *http.Transport
}

func (t *pinnedTransport) CloseIdleConnections() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, entry := range t.transports {
		entry.transport.CloseIdleConnections()
	}
}

func (t *pinnedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	originalURL := request.URL.String()
	pinnedURL, err := t.pin(request.Context(), originalURL, t.allowedPrivate)
	if err != nil {
		return nil, err
	}

	channelID := t.channelID
	if channelID == "" {
		if contextual, ok := request.Context().Value(channelIDContextKey{}).(string); ok {
			channelID = contextual
		}
	}
	for attempt := 0; attempt < maxTransportRebuilds; attempt++ {
		base := t.base
		generation := uint64(0)
		var selectedProxy *url.URL
		if t.router != nil {
			decision, selected, selectedGeneration, err := t.router.transportFor(originalURL, channelID)
			if err != nil {
				return nil, err
			}
			selectedProxy = decision.ProxyURL
			base = selected
			generation = selectedGeneration
		}
		if base == nil {
			return nil, fmt.Errorf("pinned transport requires a base transport or router")
		}
		proxyViaTransport := false
		if base.Proxy != nil {
			proxyURL, err := base.Proxy(request)
			if err != nil {
				return nil, err
			}
			selectedProxy = proxyURL
			proxyViaTransport = proxyURL != nil
		}
		proxyKey := ""
		if selectedProxy != nil {
			proxyKey = selectedProxy.String()
		}
		if proxyViaTransport && strings.EqualFold(request.URL.Scheme, "http") &&
			(strings.EqualFold(selectedProxy.Scheme, "http") || strings.EqualFold(selectedProxy.Scheme, "https")) {
			return nil, ErrUnpinnableProxyTarget
		}
		key := pinnedTransportKey{
			base:              base,
			scheme:            request.URL.Scheme,
			authority:         request.URL.Host,
			serverName:        request.URL.Hostname(),
			proxy:             proxyKey,
			proxyViaTransport: proxyViaTransport,
		}
		transport := t.transport(key, pinnedURL.Hostname(), generation)
		if transport == nil {
			continue
		}
		pinnedRequest := request.Clone(request.Context())
		pinnedRequest.URL = pinnedURL
		pinnedRequest.Host = request.URL.Host
		response, err := transport.RoundTrip(pinnedRequest)
		if response != nil {
			response.Request = request
		}
		return response, err
	}
	return nil, fmt.Errorf("pinned transport invalidated by concurrent router reloads")
}

func (t *pinnedTransport) transport(key pinnedTransportKey, pinnedHost string, generation uint64) *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if generation < t.generation {
		return nil
	}
	if generation > t.generation {
		for _, entry := range t.transports {
			entry.transport.CloseIdleConnections()
		}
		clear(t.transports)
		t.generation = generation
	}
	if entry, ok := t.transports[key]; ok {
		if entry.pinnedHost == pinnedHost {
			return entry.transport
		}
		entry.transport.CloseIdleConnections()
	}
	if len(t.transports) >= maxPinnedTransports {
		// ponytail: flush at cap; use LRU if churn matters.
		for _, entry := range t.transports {
			entry.transport.CloseIdleConnections()
		}
		clear(t.transports)
	}
	transport := key.base.Clone()
	if key.proxyViaTransport {
		proxyURL, err := url.Parse(key.proxy)
		if err != nil {
			return nil
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	} else if key.proxy == "" {
		transport.Proxy = nil
	}
	if key.scheme == "https" && transport.TLSClientConfig == nil {
		transport.TLSClientConfig = UpstreamTLSConfig()
	}
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig.ServerName = ""
		if key.scheme == "https" {
			transport.TLSClientConfig.ServerName = key.serverName
		}
	}
	if key.scheme == "https" && key.proxyViaTransport {
		proxyURL, err := url.Parse(key.proxy)
		if err != nil {
			return nil
		}
		if strings.EqualFold(proxyURL.Scheme, "https") &&
			transport.DialTLSContext == nil && transport.DialTLS == nil { //nolint:staticcheck // Preserve the legacy hook.
			proxyTLS := transport.TLSClientConfig.Clone()
			proxyTLS.ServerName = proxyURL.Hostname()
			transport.DialTLSContext = pinnedProxyTLSDialer(
				transport.DialContext, proxyTLS, transport.TLSHandshakeTimeout,
			)
		}
	}
	t.transports[key] = pinnedTransportEntry{pinnedHost: pinnedHost, transport: transport}
	return transport
}

func pinnedProxyTLSDialer(
	dial func(context.Context, string, string) (net.Conn, error),
	config *tls.Config,
	timeout time.Duration,
) func(context.Context, string, string) (net.Conn, error) {
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		handshakeCtx := ctx
		cancel := func() {}
		if timeout > 0 {
			handshakeCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		defer cancel()
		tlsConnection := tls.Client(connection, config.Clone())
		if err := tlsConnection.HandshakeContext(handshakeCtx); err != nil {
			_ = connection.Close()
			return nil, err
		}
		return tlsConnection, nil
	}
}
