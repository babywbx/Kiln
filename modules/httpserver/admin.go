package httpserver

import (
	"encoding/json"
	"io"
	"net/http"
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
	}
	if err := s.deps.Catalog.Upsert(ch); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": ch.ID})
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
	_ = s.deps.Sessions.StopChannel(id)
	if err := s.deps.Catalog.Delete(id); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
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
	Name       string   `json:"name"`
	Note       string   `json:"note"`
	ChannelIDs []string `json:"channel_ids"`
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
	}
	out := make([]tv, 0, len(rows))
	for _, row := range rows {
		out = append(out, tv{
			ID: row.ID, Name: row.Name, Prefix: row.Prefix, Scope: row.ScopeJSON,
			Enabled: row.Enabled && row.RevokedAt == 0, Note: row.Note,
			CreatedAt: row.CreatedAt, LastUsedAt: row.LastUsedAt, RevokedAt: row.RevokedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_tokens": out})
}

func (s *Server) handleAdminRevokeToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.deps.Store.RevokeAccessToken(id); err != nil {
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
	if err := s.deps.Store.DeleteAccessToken(id); err != nil {
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
		if m, err := s.deps.Store.ListSettings(); err == nil {
			for k, v := range m {
				out[k] = v
			}
		}
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
	var req map[string]string
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	allowed := map[string]struct{}{
		"public_base_url": {},
	}
	for k, v := range req {
		if _, ok := allowed[k]; !ok {
			continue
		}
		v = strings.TrimSpace(v)
		if k == "public_base_url" {
			v = strings.TrimRight(v, "/")
		}
		if err := s.deps.Store.SetSetting(k, v); err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
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
		UserAgent: ch.UserAgent,
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
		IDs []string `json:"ids"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil || len(req.IDs) == 0 {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "ids required"))
		return
	}
	if err := s.deps.Catalog.Reorder(req.IDs); err != nil {
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
		ch := config.Channel{
			ID: id, Title: e.Title, Group: e.Group, LogoURL: e.LogoURL,
			Upstream: up, Path: e.SuggestedPath, Ingress: ing,
			OnDemand: true, UserAgent: "Kiln/0.2",
			KeysFile: req.DefaultKeysFile, PreferHeight: req.PreferHeight,
		}
		if ch.Ingress == "dash" && ch.KeysFile == "" {
			skipped++
			continue
		}
		if err := s.deps.Catalog.Upsert(ch); err != nil {
			skipped++
			continue
		}
		created++
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
		profs, _ := s.deps.Store.ListProxyProfiles()
		rules, _ := s.deps.Store.ListProxyRules()
		def := s.deps.Cfg.Egress.Default
		pol := s.deps.Cfg.Egress.PlaylistPolicy
		dhost := s.deps.Cfg.Egress.DockerProxyHost
		if v, ok, _ := s.deps.Store.GetSetting("egress_default"); ok && v != "" {
			def = v
		}
		if v, ok, _ := s.deps.Store.GetSetting("playlist_policy"); ok && v != "" {
			pol = v
		}
		if v, ok, _ := s.deps.Store.GetSetting("docker_proxy_host"); ok && v != "" {
			dhost = v
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"default":           def,
			"playlist_policy":   pol,
			"docker_proxy_host": dhost,
			"proxies":           profs,
			"rules":             rules,
			"source":            "sqlite",
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
		"proxies":           cfg.Profiles,
		"rules":             cfg.Rules,
		"source":            "memory",
	})
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

func (s *Server) handleAdminPutEgress(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	var req struct {
		Default         string                  `json:"default"`
		PlaylistPolicy  string                  `json:"playlist_policy"`
		DockerProxyHost string                  `json:"docker_proxy_host"`
		Proxies         []store.ProxyProfileRow `json:"proxies"`
		Rules           []store.ProxyRuleRow    `json:"rules"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes*2))
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json"))
		return
	}
	if req.Default != "" {
		_ = s.deps.Store.SetSetting("egress_default", req.Default)
	}
	if req.PlaylistPolicy != "" {
		switch req.PlaylistPolicy {
		case "rewrite", "passthrough", "auto":
			_ = s.deps.Store.SetSetting("playlist_policy", req.PlaylistPolicy)
		default:
			writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "playlist_policy invalid"))
			return
		}
	}
	if req.DockerProxyHost != "" {
		_ = s.deps.Store.SetSetting("docker_proxy_host", req.DockerProxyHost)
	}
	if req.Proxies != nil {
		existing, _ := s.deps.Store.ListProxyProfiles()
		keep := map[string]struct{}{}
		for _, p := range req.Proxies {
			if p.ID == "" || p.URL == "" {
				continue
			}
			keep[p.ID] = struct{}{}
			if err := s.deps.Store.UpsertProxyProfile(p); err != nil {
				writeAppErr(w, apperr.Internal(err))
				return
			}
		}
		for _, e := range existing {
			if _, ok := keep[e.ID]; !ok {
				_ = s.deps.Store.DeleteProxyProfile(e.ID)
			}
		}
	}
	if req.Rules != nil {
		if err := s.deps.Store.ReplaceAllProxyRules(req.Rules); err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
	}
	if err := s.reloadEgressFromStore(); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
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
	if err := s.deps.Store.UpsertProxyProfile(p); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if err := s.reloadEgressFromStore(); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": p.ID})
}

func (s *Server) handleAdminDeleteProxy(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.deps.Store.DeleteProxyProfile(id); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	_ = s.reloadEgressFromStore()
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
	if err := s.deps.Store.UpsertProxyRule(rule); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if err := s.reloadEgressFromStore(); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": rule.ID})
}

func (s *Server) handleAdminDeleteRule(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := s.deps.Store.DeleteProxyRule(id); err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	_ = s.reloadEgressFromStore()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleAdminEgressTest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		URL       string `json:"url"`
		ChannelID string `json:"channel_id"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.URL) == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "url required"))
		return
	}
	if s.deps.Egress == nil {
		writeJSON(w, http.StatusOK, map[string]any{"proxy_id": "direct", "rewrite": true})
		return
	}
	d := s.deps.Egress.Resolve(req.URL, req.ChannelID)
	start := time.Now()
	res, err := s.deps.Sessions.Pull().Get(r.Context(), pull.Request{
		URL: req.URL, UserAgent: "Kiln/0.2", ChannelID: req.ChannelID,
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
		out["error"] = err.Error()
		writeJSON(w, http.StatusOK, out)
		return
	}
	defer res.Body.Close()
	_, _ = io.CopyN(io.Discard, res.Body, 2048)
	out["ok"] = true
	out["status"] = res.StatusCode
	out["final_url"] = res.FinalURL
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
