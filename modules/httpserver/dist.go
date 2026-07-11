package httpserver

import (
	"net/http"
	"strings"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/store"
)

func (s *Server) resolveAccessToken(raw string) (store.AccessTokenRow, error) {
	raw = strings.TrimSpace(raw)
	if !accesstoken.Valid(raw) {
		return store.AccessTokenRow{}, authInvalidAccess()
	}
	if s.deps.Store == nil {
		return store.AccessTokenRow{}, apperr.New(apperr.CodeInternal, 500, "store unavailable")
	}
	row, ok, err := s.deps.Store.GetAccessTokenByHash(accesstoken.Hash(raw))
	if err != nil {
		return store.AccessTokenRow{}, apperr.Internal(err)
	}
	if !ok || !row.Enabled || row.RevokedAt != 0 {
		return store.AccessTokenRow{}, authInvalidAccess()
	}
	_ = s.deps.Store.TouchAccessToken(row.ID)
	return row, nil
}

func (s *Server) logAccess(row store.AccessTokenRow, r *http.Request, channelID string, status int) {
	if s.deps.Store == nil {
		return
	}
	_ = s.deps.Store.InsertAccessLog(store.AccessLogRow{
		TokenID:     row.ID,
		TokenPrefix: row.Prefix,
		Path:        r.URL.Path,
		ChannelID:   channelID,
		Status:      status,
		Remote:      clientIP(r),
	})
}

func authInvalidAccess() error {
	return apperr.New(apperr.CodeUnauthorized, 401, "invalid access token")
}

func (s *Server) handleDistPlaylist(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("token")
	row, err := s.resolveAccessToken(raw)
	if err != nil {
		s.logAccess(store.AccessTokenRow{Prefix: accesstoken.Prefix(raw)}, r, "", 401)
		writeAppErr(w, err)
		return
	}
	chs, err := s.deps.Catalog.List(false)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	all, ids, err := accesstoken.DecodeScope(row.ScopeJSON)
	if err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "token scope invalid"))
		return
	}
	if !all {
		chs = s.deps.Catalog.FilterByIDs(chs, ids)
	}
	base := s.deps.Catalog.PublicBase()
	prefix := "/p/" + raw + "/play/"
	body := s.deps.Catalog.M3U(chs, base, prefix, "")
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
	s.deps.Observe.AddBytesOut(int64(len(body)))
	s.logAccess(row, r, "", 200)
}

func (s *Server) handleDistPlayIndex(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("token")
	row, err := s.resolveAccessToken(raw)
	if err != nil {
		s.logAccess(store.AccessTokenRow{Prefix: accesstoken.Prefix(raw)}, r, r.PathValue("id"), 401)
		writeAppErr(w, err)
		return
	}
	id := r.PathValue("id")
	if !accesstoken.AllowsChannel(row.ScopeJSON, id) {
		s.logAccess(row, r, id, 403)
		writeAppErr(w, apperr.New(apperr.CodeForbidden, 403, "channel not allowed for this link"))
		return
	}
	s.logAccess(row, r, id, 200)
	s.handlePlayIndex(w, r)
}

func (s *Server) handleDistPlayLive(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("token")
	row, err := s.resolveAccessToken(raw)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	id := r.PathValue("id")
	if !accesstoken.AllowsChannel(row.ScopeJSON, id) {
		writeAppErr(w, apperr.New(apperr.CodeForbidden, 403, "channel not allowed for this link"))
		return
	}
	s.handlePlayLiveFile(w, r)
}

func (s *Server) handleDistPlayUpstream(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("token")
	row, err := s.resolveAccessToken(raw)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	id := r.PathValue("id")
	if !accesstoken.AllowsChannel(row.ScopeJSON, id) {
		writeAppErr(w, apperr.New(apperr.CodeForbidden, 403, "channel not allowed for this link"))
		return
	}
	s.handlePlayUpstream(w, r)
}

