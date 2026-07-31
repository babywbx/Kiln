//go:build !lite

package httpserver

import (
	"net/http"

	"github.com/babywbx/kiln/modules/proxyegress"
)

type publicProbeTransport struct {
	router         *proxyegress.Router
	channelID      string
	allowedPrivate map[string]struct{}
}

func (t *publicProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return proxyegress.NewPinnedTransport(
		nil, t.router, t.channelID, t.allowedPrivate,
	).RoundTrip(request)
}
