package httpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/store"
	"github.com/babywbx/kiln/modules/version"
)

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	c := claimsFrom(r)
	if c.Role != "admin" {
		writeAppErr(w, apperr.New(apperr.CodeForbidden, 403, "admin required"))
		return false
	}
	return true
}

func (s *Server) handleAdminListChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	views, err := s.deps.Catalog.ListViews(s.deps.Catalog.PublicBase(), true)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": views})
}

func (s *Server) handleAdminGetChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var ch config.Channel
	revision := int64(0)
	updatedAt := int64(0)
	if s.deps.Store != nil {
		row, found, err := s.deps.Store.GetChannelRow(r.PathValue("id"))
		if err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
		if !found {
			writeAppErr(w, apperr.ErrNotFound)
			return
		}
		ch, revision, updatedAt = row.Channel, row.Revision, row.UpdatedAt
		w.Header().Set("ETag", strconv.FormatInt(revision, 10))
	} else {
		var found bool
		ch, found = s.deps.Catalog.GetAny(r.PathValue("id"))
		if !found {
			writeAppErr(w, apperr.ErrNotFound)
			return
		}
	}
	masked := make(map[string]string, len(ch.Headers))
	for key, value := range ch.Headers {
		if sensitiveHeader(key) {
			masked[key] = ""
			continue
		}
		masked[key] = value
	}
	ch.Headers = masked
	writeJSON(w, http.StatusOK, map[string]any{
		"channel":              ch,
		"effective_user_agent": version.UserAgent(ch.UserAgent),
		"revision":             revision,
		"updated_at":           updatedAt,
	})
}

func sensitiveHeader(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	return n == "authorization" || n == "proxy-authorization" || n == "cookie" || n == "set-cookie" ||
		strings.Contains(n, "api-key") || strings.Contains(n, "apikey") || strings.Contains(n, "token") || strings.Contains(n, "secret")
}

func publicURL(raw string, hostOnly bool) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "[redacted]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	if hostOnly {
		u.Path = ""
		u.RawPath = ""
	}
	return u.String()
}

func (s *Server) handleAdminUpsertChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var ch config.Channel
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&ch); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	if id := r.PathValue("id"); id != "" {
		ch.ID = id
		if existing, ok := s.deps.Catalog.GetAny(id); ok {
			if ch.Headers == nil {
				ch.Headers = map[string]string{}
			}
			for key, value := range existing.Headers {
				if sensitiveHeader(key) && strings.TrimSpace(ch.Headers[key]) == "" {
					ch.Headers[key] = value
				}
			}
		}
	}
	expected := expectedRevision(r)
	var err error
	if expected > 0 {
		err = s.deps.Catalog.UpsertIfRevision(ch, expected)
	} else {
		err = s.deps.Catalog.Upsert(ch)
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "channel was updated elsewhere"))
		return
	}
	if err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
		return
	}
	if ch.Disabled {
		_ = s.deps.Sessions.StopChannel(ch.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": ch.ID})
}

func expectedRevision(r *http.Request) int64 {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}

func (s *Server) handleAdminWarmupChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if err := s.deps.Sessions.Warmup(r.PathValue("id")); err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"state": "starting"})
}

func (s *Server) handleAdminStopSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	_ = s.deps.Sessions.StopChannel(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminPreviewChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.deps.Catalog.Get(id); !ok {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	token, expiresAt, err := s.deps.Auth.IssuePreview(id, 5*time.Minute)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	base := strings.TrimRight(s.deps.Catalog.PublicBase(), "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"play_url":   base + "/v1/play/" + url.PathEscape(id) + "/index.m3u8?token=" + url.QueryEscape(token),
		"expires_at": expiresAt,
	})
}

func (s *Server) handleAdminDeleteChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "id required"))
		return
	}
	expected := expectedRevision(r)
	var err error
	if expected > 0 {
		err = s.deps.Catalog.DeleteIfRevision(id, expected)
	} else {
		err = s.deps.Catalog.Delete(id)
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "channel was updated elsewhere"))
		return
	}
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	_ = s.deps.Sessions.StopChannel(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminUpstreams(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	type uv struct {
		ID      string `json:"id"`
		BaseURL string `json:"base_url"`
	}
	list := s.deps.Catalog.Upstreams()
	out := make([]uv, 0, len(list))
	for _, u := range list {
		out = append(out, uv{ID: u.ID, BaseURL: u.BaseURL})
	}
	writeJSON(w, http.StatusOK, map[string]any{"upstreams": out})
}

type createTokenReq struct {
	Name         string   `json:"name"`
	Note         string   `json:"note"`
	ChannelIDs   []string `json:"channel_ids"`
	ExpiresInSec int64    `json:"expires_in_sec"`
}

func (s *Server) handleAdminCreateToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	var req createTokenReq
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	plain, row, err := accesstoken.NewRow(req.Name, req.Note, req.ChannelIDs)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if req.ExpiresInSec < 0 || req.ExpiresInSec > int64((10*365*24*time.Hour)/time.Second) {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "expires_in_sec invalid"))
		return
	}
	if req.ExpiresInSec > 0 {
		row.ExpiresAt = time.Now().Add(time.Duration(req.ExpiresInSec) * time.Second).Unix()
	}
	if err := s.deps.Store.InsertAccessToken(row); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	base := s.deps.Catalog.PublicBase()
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":           row.ID,
		"name":         row.Name,
		"token":        plain,
		"token_prefix": row.Prefix,
		"scope":        row.ScopeJSON,
		"playlist_url": base + "/p/" + plain + "/playlist.m3u",
		"created_at":   row.CreatedAt,
		"expires_at":   row.ExpiresAt,
		"warning":      "store this token now; it will not be shown again",
	})
}

func (s *Server) handleAdminListTokens(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	rows, err := s.deps.Store.ListAccessTokens()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	type tv struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Prefix     string `json:"token_prefix"`
		Scope      string `json:"scope"`
		Enabled    bool   `json:"enabled"`
		Note       string `json:"note,omitempty"`
		CreatedAt  int64  `json:"created_at"`
		LastUsedAt int64  `json:"last_used_at,omitempty"`
		RevokedAt  int64  `json:"revoked_at,omitempty"`
		ExpiresAt  int64  `json:"expires_at,omitempty"`
		Revision   int64  `json:"revision"`
	}
	out := make([]tv, 0, len(rows))
	for _, row := range rows {
		out = append(out, tv{
			ID: row.ID, Name: row.Name, Prefix: row.Prefix, Scope: row.ScopeJSON,
			Enabled: row.Enabled && row.RevokedAt == 0, Note: row.Note,
			CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt,
			ExpiresAt: row.ExpiresAt, Revision: row.Revision,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_tokens": out})
}

func (s *Server) handleAdminRevokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	expected := expectedRevision(r)
	var err error
	if expected > 0 {
		err = s.deps.Store.RevokeAccessTokenIfRevision(id, expected)
	} else {
		err = s.deps.Store.RevokeAccessToken(id)
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "access token was updated elsewhere"))
		return
	}
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminDeleteToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	expected := expectedRevision(r)
	var err error
	if expected > 0 {
		err = s.deps.Store.DeleteAccessTokenIfRevision(id, expected)
	} else {
		err = s.deps.Store.DeleteAccessToken(id)
	}
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "access token was updated elsewhere"))
		return
	}
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminGetSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	out := map[string]any{
		"public_base_url":   s.deps.Catalog.PublicBase(),
		"listen":            s.deps.Cfg.Server.Listen,
		"cors_origins":      s.deps.Cfg.Security.CORSOrigins,
		"public_hosts":      s.deps.Cfg.Security.PublicHosts,
		"play_require_auth": s.deps.Cfg.Security.PlayRequireAuth,
	}
	if s.deps.Store != nil {
		snapshot, err := s.deps.Store.GetRuntimeSettingsSnapshot()
		if err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
		for key, value := range snapshot.Values {
			out[key] = value
		}
		out["revision"] = snapshot.Revision
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminPutSettings(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	var req struct {
		PublicBaseURL          string `json:"public_base_url"`
		AccessLogRetentionDays string `json:"access_log_retention_days"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	publicBaseURL := strings.TrimRight(strings.TrimSpace(req.PublicBaseURL), "/")
	retentionDays := strings.TrimSpace(req.AccessLogRetentionDays)
	days, err := strconv.Atoi(retentionDays)
	if err != nil || days < 1 || days > 3650 {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "access_log_retention_days invalid"))
		return
	}
	expected := expectedRevision(r)
	if expected == 0 {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "If-Match revision required"))
		return
	}
	if err := s.deps.Store.ReplaceRuntimeSettings(publicBaseURL, retentionDays, expected); err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "settings were updated elsewhere"))
		} else {
			writeAppErr(w, apperr.Internal(err))
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminProbeChannel(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	ch, ok := s.deps.Catalog.GetAny(id)
	if !ok {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	src, err := s.deps.Catalog.SourceURL(ch)
	if err != nil {
		writeAppErr(w, err)
		return
	}
	headers := s.deps.Sessions.HeadersFor(ch)
	start := time.Now()
	res, err := s.deps.Sessions.Pull().Get(r.Context(), pull.Request{
		URL:       src,
		UserAgent: version.UserAgent(ch.UserAgent),
		Headers:   headers,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"url":    src,
			"error":  err.Error(),
			"dur_ms": time.Since(start).Milliseconds(),
		})
		return
	}
	defer res.Body.Close()
	_, _ = io.CopyN(io.Discard, res.Body, 1024)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"url":          src,
		"status":       res.StatusCode,
		"content_type": res.ContentType,
		"final_url":    res.FinalURL,
		"dur_ms":       time.Since(start).Milliseconds(),
	})
}

func (s *Server) handleAdminReorderChannels(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		IDs       []string         `json:"ids"`
		Revisions map[string]int64 `json:"revisions"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil || len(req.IDs) == 0 {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "ids required"))
		return
	}
	if len(req.Revisions) == 0 {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "channel revisions required"))
		return
	}
	if err := s.deps.Catalog.ReorderIfRevisions(req.IDs, req.Revisions); errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "channel order was updated elsewhere"))
		return
	} else if err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type importM3UReq struct {
	Content         string                   `json:"content"`
	DefaultUpstream string                   `json:"default_upstream"`
	DefaultIngress  string                   `json:"default_ingress"`
	DefaultKeysFile string                   `json:"default_keys_file"`
	PreferHeight    int                      `json:"prefer_height"`
	Apply           bool                     `json:"apply"`
	Entries         []catalog.ParsedM3UEntry `json:"entries"`
	Revisions       map[string]int64         `json:"revisions"`
}

func (s *Server) handleAdminImportM3U(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req importM3UReq
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes*4))
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	entries := req.Entries
	if len(entries) == 0 {
		if strings.TrimSpace(req.Content) == "" {
			writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "content or entries required"))
			return
		}
		parsed := catalog.ParseM3U(req.Content)
		entries = catalog.SuggestImport(parsed, catalog.ImportOptions{
			DefaultUpstream: req.DefaultUpstream,
			DefaultIngress:  req.DefaultIngress,
			DefaultKeysFile: req.DefaultKeysFile,
			PreferHeight:    req.PreferHeight,
		})
	}
	if !req.Apply {
		writeJSON(w, http.StatusOK, map[string]any{
			"preview": true,
			"count":   len(entries),
			"entries": entries,
		})
		return
	}
	ups := s.deps.Catalog.Upstreams()
	if len(ups) == 0 {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "no upstreams configured"))
		return
	}
	defUp := req.DefaultUpstream
	if defUp == "" {
		defUp = ups[0].ID
	}
	created := 0
	skipped := 0
	pending := make([]config.Channel, 0, len(entries))
	for _, e := range entries {
		if e.Skip {
			skipped++
			continue
		}
		id := e.SuggestedID
		if id == "" {
			skipped++
			continue
		}
		up := e.SuggestedUpstream
		if up == "" {
			up = defUp
		}
		ing := e.SuggestedIngress
		if ing == "" {
			ing = req.DefaultIngress
		}
		if ing == "" {
			ing = "hls"
		}
		ch, exists := s.deps.Catalog.GetAny(id)
		if !exists {
			ch = config.Channel{
				ID: id, OnDemand: true, IdleTimeoutSec: 90,
				KeysFile: req.DefaultKeysFile, PreferHeight: req.PreferHeight,
			}
		}
		if e.Title != "" {
			ch.Title = e.Title
		}
		if e.Group != "" {
			ch.Group = e.Group
		}
		if e.LogoURL != "" {
			ch.LogoURL = e.LogoURL
		}
		ch.Upstream = up
		ch.Path = e.SuggestedPath
		ch.Ingress = ing
		if ch.Ingress == "dash" && ch.KeysFile == "" {
			skipped++
			continue
		}
		pending = append(pending, ch)
		created++
	}
	if err := s.deps.Catalog.UpsertBatchIfRevisions(pending, req.Revisions); errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "one or more channels were updated since the import preview"))
		return
	} else if err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"applied": true,
		"created": created,
		"skipped": skipped,
		"total":   len(entries),
	})
}

func (s *Server) handleAdminEgress(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store != nil {
		snapshot, err := s.deps.Store.GetEgressSnapshot()
		if err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
		def := firstNonEmpty(snapshot.Default, s.deps.Cfg.Egress.Default, proxyegress.Direct)
		pol := firstNonEmpty(snapshot.PlaylistPolicy, s.deps.Cfg.Egress.PlaylistPolicy, string(proxyegress.PolicyRewrite))
		dhost := firstNonEmpty(snapshot.DockerProxyHost, s.deps.Cfg.Egress.DockerProxyHost, "host.docker.internal")
		writeJSON(w, http.StatusOK, map[string]any{
			"default":           def,
			"playlist_policy":   pol,
			"docker_proxy_host": dhost,
			"proxies":           publicProxyRows(snapshot.Profiles),
			"rules":             snapshot.Rules,
			"source":            "sqlite",
			"revision":          snapshot.Revision,
		})
		return
	}
	if s.deps.Egress == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"default": "direct", "playlist_policy": "rewrite", "proxies": []any{}, "rules": []any{},
		})
		return
	}
	cfg := s.deps.Egress.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"default":           cfg.Default,
		"playlist_policy":   cfg.PlaylistPolicy,
		"docker_proxy_host": cfg.DockerProxyHost,
		"proxies":           publicProxyProfiles(cfg.Profiles),
		"rules":             cfg.Rules,
		"source":            "memory",
	})
}

func publicProxyRows(rows []store.ProxyProfileRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		u, _ := url.Parse(row.URL)
		out = append(out, map[string]any{
			"id": row.ID, "name": row.Name, "url": publicURL(row.URL, true), "disabled": row.Disabled,
			"credential_configured": u != nil && u.User != nil, "revision": row.Revision,
		})
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func publicProxyProfiles(rows []proxyegress.Profile) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		u, _ := url.Parse(row.URL)
		out = append(out, map[string]any{
			"id": row.ID, "name": row.Name, "url": publicURL(row.URL, true), "disabled": row.Disabled,
			"credential_configured": u != nil && u.User != nil,
		})
	}
	return out
}

func (s *Server) reloadEgressFromStore() error {
	if s.deps.Egress == nil || s.deps.Store == nil {
		return nil
	}
	cfg, err := proxyegress.ConfigFromStore(s.deps.Store, s.deps.Cfg)
	if err != nil {
		return err
	}
	return s.deps.Egress.Reload(cfg)
}

type egressDraft struct {
	Default         string                  `json:"default"`
	PlaylistPolicy  string                  `json:"playlist_policy"`
	DockerProxyHost string                  `json:"docker_proxy_host"`
	Proxies         []store.ProxyProfileRow `json:"proxies"`
	Rules           []store.ProxyRuleRow    `json:"rules"`
}

func normalizeEgressDraft(draft egressDraft, existing []store.ProxyProfileRow) (egressDraft, proxyegress.Config, error) {
	if draft.Default == "" {
		draft.Default = proxyegress.Direct
	}
	if draft.PlaylistPolicy == "" {
		draft.PlaylistPolicy = string(proxyegress.PolicyRewrite)
	}
	if draft.DockerProxyHost == "" {
		draft.DockerProxyHost = "host.docker.internal"
	}
	switch proxyegress.PlaylistPolicy(draft.PlaylistPolicy) {
	case proxyegress.PolicyRewrite, proxyegress.PolicyPassthrough, proxyegress.PolicyAuto:
	default:
		return egressDraft{}, proxyegress.Config{}, errors.New("playlist_policy invalid")
	}
	existingByID := make(map[string]store.ProxyProfileRow, len(existing))
	for _, profile := range existing {
		existingByID[profile.ID] = profile
	}
	profileIDs := map[string]struct{}{proxyegress.Direct: {}}
	cfg := proxyegress.Config{
		Default: draft.Default, PlaylistPolicy: proxyegress.PlaylistPolicy(draft.PlaylistPolicy),
		DockerProxyHost: draft.DockerProxyHost,
	}
	for i := range draft.Proxies {
		profile := &draft.Proxies[i]
		profile.ID = strings.TrimSpace(profile.ID)
		profile.URL = strings.TrimSpace(profile.URL)
		if profile.ID == "" || profile.URL == "" || profile.ID == proxyegress.Direct {
			return egressDraft{}, proxyegress.Config{}, errors.New("each proxy requires a unique non-direct id and url")
		}
		if _, duplicate := profileIDs[profile.ID]; duplicate {
			return egressDraft{}, proxyegress.Config{}, fmt.Errorf("duplicate proxy id %q", profile.ID)
		}
		incomingURL, err := url.Parse(profile.URL)
		if err != nil || incomingURL.Host == "" {
			return egressDraft{}, proxyegress.Config{}, fmt.Errorf("proxy %q url is invalid", profile.ID)
		}
		if previous, ok := existingByID[profile.ID]; ok && incomingURL.User == nil {
			previousURL, parseErr := url.Parse(previous.URL)
			if parseErr == nil && previousURL.User != nil && incomingURL.Scheme == previousURL.Scheme && incomingURL.Host == previousURL.Host {
				incomingURL.User = previousURL.User
				profile.URL = incomingURL.String()
			}
		}
		profileIDs[profile.ID] = struct{}{}
		cfg.Profiles = append(cfg.Profiles, proxyegress.Profile{ID: profile.ID, Name: profile.Name, URL: profile.URL, Disabled: profile.Disabled})
	}
	ruleIDs := map[string]struct{}{}
	for i := range draft.Rules {
		rule := &draft.Rules[i]
		rule.ID = strings.TrimSpace(rule.ID)
		if rule.ID == "" {
			return egressDraft{}, proxyegress.Config{}, errors.New("each rule requires a unique id")
		}
		if _, duplicate := ruleIDs[rule.ID]; duplicate {
			return egressDraft{}, proxyegress.Config{}, fmt.Errorf("duplicate rule id %q", rule.ID)
		}
		ruleIDs[rule.ID] = struct{}{}
		if rule.Kind == "" {
			rule.Kind = string(proxyegress.KindHostSuffix)
		}
		if rule.ProxyID == "" {
			rule.ProxyID = proxyegress.Direct
		}
		if _, ok := profileIDs[rule.ProxyID]; !ok {
			return egressDraft{}, proxyegress.Config{}, fmt.Errorf("rule %q references unknown proxy %q", rule.ID, rule.ProxyID)
		}
		switch proxyegress.RuleKind(rule.Kind) {
		case proxyegress.KindHostSuffix, proxyegress.KindHostExact, proxyegress.KindChannel:
		case proxyegress.KindHostRegex, proxyegress.KindURLRegex:
			if _, err := regexp.Compile(rule.Pattern); err != nil {
				return egressDraft{}, proxyegress.Config{}, fmt.Errorf("rule %q pattern: %w", rule.ID, err)
			}
		default:
			return egressDraft{}, proxyegress.Config{}, fmt.Errorf("rule %q has invalid kind", rule.ID)
		}
		cfg.Rules = append(cfg.Rules, proxyegress.Rule{ID: rule.ID, Priority: rule.Priority, Kind: proxyegress.RuleKind(rule.Kind), Pattern: rule.Pattern, ProxyID: rule.ProxyID, Disabled: rule.Disabled})
	}
	if _, err := proxyegress.NewRouter(cfg); err != nil {
		return egressDraft{}, proxyegress.Config{}, err
	}
	return draft, cfg, nil
}

func (s *Server) currentEgressDraft() (egressDraft, int64, error) {
	snapshot, err := s.deps.Store.GetEgressSnapshot()
	if err != nil {
		return egressDraft{}, 0, err
	}
	return egressDraft{
		Default:         firstNonEmpty(snapshot.Default, s.deps.Cfg.Egress.Default, proxyegress.Direct),
		PlaylistPolicy:  firstNonEmpty(snapshot.PlaylistPolicy, s.deps.Cfg.Egress.PlaylistPolicy, string(proxyegress.PolicyRewrite)),
		DockerProxyHost: firstNonEmpty(snapshot.DockerProxyHost, s.deps.Cfg.Egress.DockerProxyHost, "host.docker.internal"),
		Proxies:         snapshot.Profiles,
		Rules:           snapshot.Rules,
	}, snapshot.Revision, nil
}

func (s *Server) applyEgressDraft(draft egressDraft, existing []store.ProxyProfileRow, expected int64) error {
	if expected == 0 {
		return store.ErrRevisionConflict
	}
	normalized, _, err := normalizeEgressDraft(draft, existing)
	if err != nil {
		return err
	}
	if err := s.deps.Store.ReplaceEgressConfiguration(normalized.Default, normalized.PlaylistPolicy, normalized.DockerProxyHost, normalized.Proxies, normalized.Rules, expected); err != nil {
		return err
	}
	return s.reloadEgressFromStore()
}

func writeEgressApplyError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, http.StatusConflict, "egress configuration was updated elsewhere or If-Match is missing"))
		return
	}
	writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
}

func (s *Server) handleAdminPutEgress(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	var req egressDraft
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes*2))
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	current, _, err := s.currentEgressDraft()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	expected := expectedRevision(r)
	if err := s.applyEgressDraft(req, current.Proxies, expected); err != nil {
		writeEgressApplyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminUpsertProxy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p store.ProxyProfileRow
	if err := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes)).Decode(&p); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	if id := r.PathValue("id"); id != "" {
		p.ID = id
	}
	if p.ID == "" || p.URL == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "id and url required"))
		return
	}
	draft, _, err := s.currentEgressDraft()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	existing := append([]store.ProxyProfileRow(nil), draft.Proxies...)
	replaced := false
	for i := range draft.Proxies {
		if draft.Proxies[i].ID == p.ID {
			draft.Proxies[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		draft.Proxies = append(draft.Proxies, p)
	}
	if err := s.applyEgressDraft(draft, existing, expectedRevision(r)); err != nil {
		writeEgressApplyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID})
}

func (s *Server) handleAdminDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	draft, _, err := s.currentEgressDraft()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	existing := append([]store.ProxyProfileRow(nil), draft.Proxies...)
	profiles := draft.Proxies[:0]
	for _, profile := range draft.Proxies {
		if profile.ID != id {
			profiles = append(profiles, profile)
		}
	}
	draft.Proxies = profiles
	rules := draft.Rules[:0]
	for _, rule := range draft.Rules {
		if rule.ProxyID != id {
			rules = append(rules, rule)
		}
	}
	draft.Rules = rules
	if draft.Default == id {
		draft.Default = proxyegress.Direct
	}
	if err := s.applyEgressDraft(draft, existing, expectedRevision(r)); err != nil {
		writeEgressApplyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminUpsertRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var rule store.ProxyRuleRow
	if err := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes)).Decode(&rule); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	if id := r.PathValue("id"); id != "" {
		rule.ID = id
	}
	if rule.ID == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "id required"))
		return
	}
	draft, _, err := s.currentEgressDraft()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	replaced := false
	for i := range draft.Rules {
		if draft.Rules[i].ID == rule.ID {
			draft.Rules[i] = rule
			replaced = true
			break
		}
	}
	if !replaced {
		draft.Rules = append(draft.Rules, rule)
	}
	if err := s.applyEgressDraft(draft, draft.Proxies, expectedRevision(r)); err != nil {
		writeEgressApplyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": rule.ID})
}

func (s *Server) handleAdminDeleteRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	draft, _, err := s.currentEgressDraft()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	rules := draft.Rules[:0]
	for _, rule := range draft.Rules {
		if rule.ID != id {
			rules = append(rules, rule)
		}
	}
	draft.Rules = rules
	if err := s.applyEgressDraft(draft, draft.Proxies, expectedRevision(r)); err != nil {
		writeEgressApplyError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminEgressTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		URL       string       `json:"url"`
		ChannelID string       `json:"channel_id"`
		Draft     *egressDraft `json:"draft,omitempty"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "url required"))
		return
	}
	testRouter := s.deps.Egress
	if req.Draft != nil {
		if s.deps.Store == nil {
			writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
			return
		}
		existing, err := s.deps.Store.ListProxyProfiles()
		if err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
		_, cfg, err := normalizeEgressDraft(*req.Draft, existing)
		if err != nil {
			writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
			return
		}
		testRouter, err = proxyegress.NewRouter(cfg)
		if err != nil {
			writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
			return
		}
	}
	if testRouter == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "proxy_id": "direct", "rewrite": true})
		return
	}
	d := testRouter.Resolve(req.URL, req.ChannelID)
	start := time.Now()
	testPull := s.deps.Sessions.Pull()
	if req.Draft != nil {
		testPull = pull.New(pull.Options{
			Observe: s.deps.Observe, Allowed: s.deps.Allowed,
			MaxPlaylist: s.deps.Cfg.Security.MaxPlaylistBytes, Router: testRouter,
		})
	}
	res, err := testPull.Get(r.Context(), pull.Request{
		URL: req.URL, UserAgent: version.UserAgent(""), ChannelID: req.ChannelID,
	})
	out := map[string]any{
		"proxy_id": d.ProxyID,
		"reason":   d.Reason,
		"rewrite":  d.Rewrite,
		"dur_ms":   time.Since(start).Milliseconds(),
	}
	if d.ProxyURL != nil {
		out["proxy_url"] = d.ProxyURL.Scheme + "://" + d.ProxyURL.Host
	}
	if err != nil {
		out["ok"] = false
		out["error"] = strings.ReplaceAll(err.Error(), req.URL, "[redacted-url]")
		writeJSON(w, http.StatusOK, out)
		return
	}
	defer res.Body.Close()
	_, _ = io.CopyN(io.Discard, res.Body, 2048)
	out["ok"] = true
	out["status"] = res.StatusCode
	out["final_url"] = publicURL(res.FinalURL, false)
	out["via_proxy"] = res.ProxyID
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminAccessLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	tokenID := r.URL.Query().Get("token_id")
	rows, err := s.deps.Store.ListAccessLogs(limit, tokenID)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_logs": rows})
}

func (s *Server) handleAdminClearAccessLogs(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	deleted, err := s.deps.Store.ClearAccessLogs()
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}
