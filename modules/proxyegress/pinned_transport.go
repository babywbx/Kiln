package proxyegress

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/security"
)

func NewPinnedTransport(
	base *http.Transport,
	router *Router,
	channelID string,
	allowedPrivate map[string]struct{},
) http.RoundTripper {
	return &pinnedTransport{
		base:           base,
		router:         router,
		channelID:      channelID,
		allowedPrivate: allowedPrivate,
	}
}

type pinnedTransport struct {
	base           *http.Transport
	router         *Router
	channelID      string
	allowedPrivate map[string]struct{}
}

func (t *pinnedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	originalURL := request.URL.String()
	pinnedURL, err := security.PinPublicProbeURL(request.Context(), originalURL, t.allowedPrivate)
	if err != nil {
		return nil, err
	}

	base := t.base
	if t.router != nil {
		decision := t.router.Resolve(originalURL, t.channelID)
		client, err := t.router.ClientForProxy(decision.ProxyID, 10*time.Second)
		if err != nil {
			return nil, err
		}
		var ok bool
		base, ok = client.Transport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("unsupported pinned transport %T", client.Transport)
		}
	}
	if base == nil {
		return nil, fmt.Errorf("pinned transport requires a base transport or router")
	}
	transport := base.Clone()
	transport.DisableKeepAlives = true
	var proxyURL *url.URL
	if transport.Proxy != nil {
		proxyURL, err = transport.Proxy(request)
		if err != nil {
			return nil, err
		}
	}
	if request.URL.Scheme == "https" {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.ServerName = request.URL.Hostname()
		if proxyURL != nil && strings.EqualFold(proxyURL.Scheme, "https") {
			dial := transport.DialContext
			if dial == nil {
				dial = (&net.Dialer{}).DialContext
			}
			proxyTLS := transport.TLSClientConfig.Clone()
			proxyTLS.ServerName = proxyURL.Hostname()
			transport.DialTLSContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				connection, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return tls.Client(connection, proxyTLS), nil
			}
		}
	}

	pinnedRequest := request.Clone(request.Context())
	pinnedRequest.URL = pinnedURL
	pinnedRequest.Host = request.URL.Host
	if request.URL.Scheme == "http" && transport.Proxy != nil {
		if proxyURL != nil {
			pinnedRequest.URL.Opaque = "//" + pinnedURL.Host + pinnedURL.RequestURI()
		}
	}
	response, err := transport.RoundTrip(pinnedRequest)
	if response != nil {
		response.Request = request
	}
	return response, err
}
