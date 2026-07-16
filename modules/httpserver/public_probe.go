package httpserver

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"time"

	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/security"
)

type publicProbeTransport struct {
	router         *proxyegress.Router
	channelID      string
	allowedPrivate map[string]struct{}
}

func (t *publicProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	originalURL := request.URL.String()
	pinnedURL, err := security.PinPublicProbeURL(request.Context(), originalURL, t.allowedPrivate)
	if err != nil {
		return nil, err
	}
	decision := t.router.Resolve(originalURL, t.channelID)
	client, err := t.router.ClientForProxy(decision.ProxyID, 10*time.Second)
	if err != nil {
		return nil, err
	}
	base, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unsupported public probe transport %T", client.Transport)
	}
	transport := base.Clone()
	transport.DisableKeepAlives = true
	if request.URL.Scheme == "https" {
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			transport.TLSClientConfig = transport.TLSClientConfig.Clone()
		}
		transport.TLSClientConfig.ServerName = request.URL.Hostname()
	}

	pinnedRequest := request.Clone(request.Context())
	pinnedRequest.URL = pinnedURL
	pinnedRequest.Host = request.URL.Host
	if decision.ProxyURL != nil && request.URL.Scheme == "http" {
		switch decision.ProxyURL.Scheme {
		case "http", "https":
			// Go normally uses Request.Host for both the Host header and the
			// absolute-form proxy target. Opaque keeps the proxy target pinned
			// to the verified IP so the proxy cannot resolve the original name.
			pinnedRequest.URL.Opaque = "//" + pinnedURL.Host + pinnedURL.RequestURI()
		}
	}
	response, err := transport.RoundTrip(pinnedRequest)
	if response != nil {
		response.Request = request
	}
	return response, err
}
