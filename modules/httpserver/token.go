package httpserver

import (
	"net/http"

	"github.com/babywbx/kiln/modules/auth"
)

func extractPlayToken(r *http.Request) string {
	if token := r.URL.Query().Get("token"); token != "" {
		return token
	}
	return auth.BearerToken(r.Header.Get("Authorization"))
}
