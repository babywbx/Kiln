package proxyegress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"sync"

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

const maxTransportRebuilds = 4

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
	base                  *http.Transport
	scheme                string
	authority             string
	serverName            string
	proxy                 string
	proxyResolvesHostname bool
}

type pinnedTransportEntry struct {
	pinnedHost string
	transport  *http.Transport
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
		proxyResolvesHostname := false
		proxyKey := ""
		if t.router != nil {
			decision, selected, selectedGeneration, err := t.router.transportFor(originalURL, channelID)
			if err != nil {
				return nil, err
			}
			proxyResolvesHostname = ProxyResolvesHostname(decision.ProxyURL)
			if decision.ProxyURL != nil {
				proxyKey = decision.ProxyURL.String()
			}
			base = selected
			generation = selectedGeneration
		}
		if base == nil {
			return nil, fmt.Errorf("pinned transport requires a base transport or router")
		}
		if base.Proxy != nil {
			proxyURL, err := base.Proxy(request)
			if err != nil {
				return nil, err
			}
			if proxyURL != nil {
				proxyKey = proxyURL.String()
				proxyResolvesHostname = ProxyResolvesHostname(proxyURL)
			}
		}
		key := pinnedTransportKey{
			base:                  base,
			scheme:                request.URL.Scheme,
			authority:             request.URL.Host,
			serverName:            request.URL.Hostname(),
			proxy:                 proxyKey,
			proxyResolvesHostname: proxyResolvesHostname,
		}
		transport := t.transport(key, pinnedURL.Hostname(), generation)
		if transport == nil {
			continue
		}
		pinnedRequest := request
		if !proxyResolvesHostname {
			pinnedRequest = request.Clone(request.Context())
			pinnedRequest.URL = pinnedURL
			pinnedRequest.Host = request.URL.Host
		}
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
	transport := key.base.Clone()
	if key.scheme == "https" && transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	if transport.TLSClientConfig != nil {
		transport.TLSClientConfig.ServerName = ""
		if !key.proxyResolvesHostname {
			transport.TLSClientConfig.ServerName = key.serverName
		}
	}
	t.transports[key] = pinnedTransportEntry{pinnedHost: pinnedHost, transport: transport}
	return transport
}
