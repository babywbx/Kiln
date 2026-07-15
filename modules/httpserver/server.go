package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/admintoken"
	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/egress"
	"github.com/babywbx/kiln/modules/epg"
	"github.com/babywbx/kiln/modules/logging"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/packager"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/security"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
	"github.com/babywbx/kiln/modules/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
)

type Deps struct {
	Cfg      config.File
	Auth     *auth.Service
	Catalog  *catalog.Service
	Sessions *session.Manager
	Observe  *observe.Service
	Store    *store.DB
	EPG      *epg.Service
	Egress   *proxyegress.Router
	Log      *slog.Logger
	Allowed  map[string]struct{}
}

type Server struct {
	deps   Deps
	mux    *http.ServeMux
	http   *http.Server
	loginL *security.Limiter
}

const llhlsWaitTimeout = 15 * time.Second

func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	s := &Server{
		deps:   deps,
		mux:    http.NewServeMux(),
		loginL: security.NewLimiter(deps.Cfg.Auth.LoginRatePerMin),
	}
	s.routes()
	handler := s.withMiddleware(s.mux)
	s.http = &http.Server{
		Addr:              deps.Cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(deps.Cfg.Server.ReadTimeout) * time.Second,
		IdleTimeout:       time.Duration(deps.Cfg.Server.IdleTimeout) * time.Second,
	}
	if deps.Cfg.Server.WriteTimeout > 0 {
		s.http.WriteTimeout = time.Duration(deps.Cfg.Server.WriteTimeout) * time.Second
	}
	return s
}

func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) Start() error {
	s.deps.Log.Info("listening", "addr", s.deps.Cfg.Server.Listen, "version", version.Version)
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealth)
	s.mux.HandleFunc("GET /readyz", s.handleReady)
	s.mux.HandleFunc("GET /metrics", s.handleMetrics)
	s.mux.HandleFunc("GET /", s.handleRoot)
	s.mux.HandleFunc("GET /admin", s.handleAdminUI)
	s.mux.HandleFunc("GET /admin/", s.handleAdminUI)

	s.mux.HandleFunc("POST /v1/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /v1/me", s.requireAuth(s.handleMe))
	s.mux.HandleFunc("PUT /v1/me/credentials", s.requireAuth(s.handleUpdateCredentials))
	s.mux.HandleFunc("GET /v1/channels", s.requireAuth(s.handleChannels))
	s.mux.HandleFunc("GET /v1/status", s.requireAuth(s.handleStatus))
	s.mux.HandleFunc("GET /v1/playlist.m3u", s.requireAuth(s.handlePlaylist))
	s.mux.HandleFunc("GET /v1/epg.xml", s.handleEPGXML)
	s.mux.HandleFunc("GET /v1/epg.xml.gz", s.handleEPGGzip)
	s.mux.HandleFunc("GET /v1/logo/{id}", s.handleChannelLogo)

	s.mux.HandleFunc("GET /v1/admin/channels", s.requireAuth(s.handleAdminListChannels))
	s.mux.HandleFunc("GET /v1/admin/channels/{id}", s.requireAuth(s.handleAdminGetChannel))
	s.mux.HandleFunc("POST /v1/admin/channels", s.requireAuth(s.handleAdminUpsertChannel))
	s.mux.HandleFunc("PUT /v1/admin/channels/{id}", s.requireAuth(s.handleAdminUpsertChannel))
	s.mux.HandleFunc("DELETE /v1/admin/channels/{id}", s.requireAuth(s.handleAdminDeleteChannel))
	s.mux.HandleFunc("POST /v1/admin/channels/enable-all", s.requireAuth(s.handleAdminEnableAllChannels))
	s.mux.HandleFunc("POST /v1/admin/channels/disable-all", s.requireAuth(s.handleAdminDisableAllChannels))
	s.mux.HandleFunc("GET /v1/admin/epg/presets", s.requireAuth(s.handleAdminEPGPresets))
	s.mux.HandleFunc("GET /v1/admin/epg/sources", s.requireAuth(s.handleAdminEPGSources))
	s.mux.HandleFunc("POST /v1/admin/epg/sources", s.requireAuth(s.handleAdminCreateEPGSource))
	s.mux.HandleFunc("PUT /v1/admin/epg/sources/{id}", s.requireAuth(s.handleAdminUpdateEPGSource))
	s.mux.HandleFunc("DELETE /v1/admin/epg/sources/{id}", s.requireAuth(s.handleAdminDeleteEPGSource))
	s.mux.HandleFunc("GET /v1/admin/epg/matches", s.requireAuth(s.handleAdminEPGMatches))
	s.mux.HandleFunc("POST /v1/admin/epg/refresh", s.requireAuth(s.handleAdminEPGRefresh))
	s.mux.HandleFunc("GET /v1/admin/upstreams", s.requireAuth(s.handleAdminUpstreams))
	s.mux.HandleFunc("GET /v1/admin/access-tokens", s.requireAuth(s.handleAdminListTokens))
	s.mux.HandleFunc("POST /v1/admin/access-tokens", s.requireAuth(s.handleAdminCreateToken))
	s.mux.HandleFunc("POST /v1/admin/access-tokens/{id}/revoke", s.requireAuth(s.handleAdminRevokeToken))
	s.mux.HandleFunc("DELETE /v1/admin/access-tokens/{id}", s.requireAuth(s.handleAdminDeleteToken))
	s.mux.HandleFunc("GET /v1/admin/settings", s.requireAuth(s.handleAdminGetSettings))
	s.mux.HandleFunc("PUT /v1/admin/settings", s.requireAuth(s.handleAdminPutSettings))
	s.mux.HandleFunc("POST /v1/admin/channels/{id}/probe", s.requireAuth(s.handleAdminProbeChannel))
	s.mux.HandleFunc("POST /v1/admin/source-probes", s.requireAuth(s.handleAdminProbeSource))
	s.mux.HandleFunc("POST /v1/admin/channels/{id}/warmup", s.requireAuth(s.handleAdminWarmupChannel))
	s.mux.HandleFunc("POST /v1/admin/channels/{id}/preview", s.requireAuth(s.handleAdminPreviewChannel))
	s.mux.HandleFunc("DELETE /v1/admin/sessions/{id}", s.requireAuth(s.handleAdminStopSession))
	s.mux.HandleFunc("PUT /v1/admin/channels/reorder", s.requireAuth(s.handleAdminReorderChannels))
	s.mux.HandleFunc("POST /v1/admin/import/m3u", s.requireAuth(s.handleAdminImportM3U))
	s.mux.HandleFunc("POST /v1/admin/exports/m3u", s.requireAuth(s.handleAdminExportM3U))
	s.mux.HandleFunc("GET /v1/admin/access-logs", s.requireAuth(s.handleAdminAccessLogs))
	s.mux.HandleFunc("DELETE /v1/admin/access-logs", s.requireAuth(s.handleAdminClearAccessLogs))
	s.mux.HandleFunc("GET /v1/admin/egress", s.requireAuth(s.handleAdminEgress))
	s.mux.HandleFunc("PUT /v1/admin/egress", s.requireAuth(s.handleAdminPutEgress))
	s.mux.HandleFunc("POST /v1/admin/egress/proxies", s.requireAuth(s.handleAdminUpsertProxy))
	s.mux.HandleFunc("PUT /v1/admin/egress/proxies/{id}", s.requireAuth(s.handleAdminUpsertProxy))
	s.mux.HandleFunc("DELETE /v1/admin/egress/proxies/{id}", s.requireAuth(s.handleAdminDeleteProxy))
	s.mux.HandleFunc("POST /v1/admin/egress/rules", s.requireAuth(s.handleAdminUpsertRule))
	s.mux.HandleFunc("PUT /v1/admin/egress/rules/{id}", s.requireAuth(s.handleAdminUpsertRule))
	s.mux.HandleFunc("DELETE /v1/admin/egress/rules/{id}", s.requireAuth(s.handleAdminDeleteRule))
	s.mux.HandleFunc("POST /v1/admin/egress/test", s.requireAuth(s.handleAdminEgressTest))
	s.mux.HandleFunc("GET /v1/admin/api-tokens", s.requireAuth(s.handleAdminListAPITokens))
	s.mux.HandleFunc("POST /v1/admin/api-tokens", s.requireAuth(s.handleAdminCreateAPIToken))
	s.mux.HandleFunc("PUT /v1/admin/api-tokens/{id}", s.requireAuth(s.handleAdminUpdateAPIToken))
	s.mux.HandleFunc("POST /v1/admin/api-tokens/{id}/rotate", s.requireAuth(s.handleAdminRotateAPIToken))
	s.mux.HandleFunc("POST /v1/admin/api-tokens/{id}/revoke", s.requireAuth(s.handleAdminRevokeAPIToken))
	s.mux.HandleFunc("DELETE /v1/admin/api-tokens/{id}", s.requireAuth(s.handleAdminDeleteAPIToken))
	s.mux.HandleFunc("GET /v1/admin/api-token-logs", s.requireAuth(s.handleAdminAPITokenLogs))

	s.mux.HandleFunc("GET /p/{token}/playlist.m3u", s.handleDistPlaylist)
	s.mux.HandleFunc("GET /p/{token}/play/{id}/index.m3u8", s.handleDistPlayIndex)
	s.mux.HandleFunc("GET /p/{token}/play/{id}/live/{file}", s.handleDistPlayLive)
	s.mux.HandleFunc("GET /p/{token}/play/{id}/u/{upstream}", s.handleDistPlayUpstream)

	s.mux.HandleFunc("GET /v1/play/{id}/index.m3u8", s.requirePlayAuth(s.handlePlayIndex))
	s.mux.HandleFunc("GET /v1/play/{id}/live/{file}", s.requirePlayAuth(s.handlePlayLiveFile))
	s.mux.HandleFunc("GET /v1/play/{id}/u/{upstream}", s.requirePlayAuth(s.handlePlayUpstream))
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceContext := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		traceContext, span := otel.Tracer("kiln/httpserver").Start(traceContext, "http.server")
		r = r.WithContext(traceContext)
		span.SetAttributes(attribute.String("http.request.method", r.Method))
		start := time.Now()
		s.deps.Observe.IncRequest()
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			reqID = randomID()
			r.Header.Set("X-Request-ID", reqID)
		}
		ww := &statusWriter{ResponseWriter: w, code: 200}
		ww.Header().Set("X-Request-ID", reqID)
		ww.Header().Set("X-Content-Type-Options", "nosniff")
		ww.Header().Set("Referrer-Policy", "no-referrer")
		ww.Header().Set("X-Frame-Options", "DENY")
		ww.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data: http: https:; media-src 'self' blob:; connect-src 'self'; worker-src 'self' blob:; font-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'")
		ww.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		ww.Header().Set("Pragma", "no-cache")
		s.applyCORS(ww, r)

		defer func() {
			abortResponse := false
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					abortResponse = true
				} else {
					s.deps.Observe.IncError()
					s.deps.Log.Error("panic",
						"request_id", reqID,
						"path", redactRequestPath(r.URL.Path),
						"panic", rec,
					)
					writeAppErr(ww, apperr.Internal(nil))
				}
			}
			level := logging.AccessLevel(r.URL.Path, ww.code)
			span.SetAttributes(attribute.Int("http.response.status_code", ww.code))
			if r.Pattern != "" {
				span.SetAttributes(attribute.String("http.route", r.Pattern))
			}
			if ww.code >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(ww.code))
			}
			span.End()
			s.deps.Log.Log(r.Context(), level, "request",
				"remote", clientIP(r),
				"method", r.Method,
				"path", redactRequestPath(r.URL.Path),
				"status", ww.code,
				"dur_ms", time.Since(start).Milliseconds(),
				"request_id", reqID,
			)
			if abortResponse {
				panic(http.ErrAbortHandler)
			}
		}()

		if r.Method == http.MethodOptions {
			ww.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.hostAllowed(r) {
			writeAppErr(ww, apperr.New(apperr.CodeForbidden, 403, "host not allowed"))
			return
		}
		next.ServeHTTP(ww, r)
	})
}

func (s *Server) applyCORS(w http.ResponseWriter, r *http.Request) {
	origins := s.deps.Cfg.Security.CORSOrigins
	if len(origins) == 0 {
		return
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	allow := false
	for _, o := range origins {
		if o == "*" || strings.EqualFold(o, origin) {
			allow = true
			if o != "*" {
				origin = o
			}
			break
		}
	}
	if !allow {
		return
	}
	if origins[0] == "*" && len(origins) == 1 {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
	w.Header().Set("Access-Control-Max-Age", "600")
}

func (s *Server) hostAllowed(r *http.Request) bool {
	hosts := s.deps.Cfg.Security.PublicHosts
	if len(hosts) == 0 {
		return true
	}
	h := r.Host
	if i := strings.Index(h, ":"); i >= 0 {
		h = h[:i]
	}
	h = strings.ToLower(h)
	for _, allow := range hosts {
		allow = strings.ToLower(strings.TrimSpace(allow))
		if allow == "" {
			continue
		}
		if allow == h || allow == "*" {
			return true
		}
	}
	return false
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/admin", http.StatusFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    "kiln",
		"version": version.Version,
		"commit":  version.Commit,
		"admin":   "/admin",
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	needFF := false
	chs, _ := s.deps.Catalog.List(false)
	for _, ch := range chs {
		if ch.Ingress == "dash" {
			needFF = true
			break
		}
	}
	if needFF {
		dependency := s.deps.Cfg.FFmpeg.Dependency()
		if _, err := exec.LookPath(dependency); err != nil {
			if _, statErr := os.Stat(dependency); statErr != nil {
				writeAppErr(w, apperr.New(apperr.CodeNotReady, 503, "ffmpeg not available"))
				return
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.loginL.Allow(ip) {
		writeAppErr(w, apperr.ErrTooMany)
		return
	}
	var req loginReq
	dec := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "invalid json body"))
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "username and password required"))
		return
	}
	res, err := s.deps.Auth.Login(req.Username, req.Password)
	if err != nil {
		s.deps.Observe.IncError()
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c := claimsFrom(r)
	p := principalFrom(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"username":    p.Subject,
		"role":        p.Role,
		"channel_ids": c.ChannelIDs,
		"credential":  p.Kind,
		"scopes":      p.Scopes,
	})
}

func (s *Server) handleUpdateCredentials(w http.ResponseWriter, r *http.Request) {
	if !s.requireSessionAdmin(w, r) {
		return
	}
	claims := claimsFrom(r)
	if !s.loginL.Allow("credentials:" + claims.Username() + ":" + clientIP(r)) {
		writeAppErr(w, apperr.ErrTooMany)
		return
	}
	if s.deps.Store == nil {
		writeAppErr(w, apperr.New(apperr.CodeInternal, 500, "store unavailable"))
		return
	}
	var request struct {
		CurrentPassword string `json:"current_password"`
		Username        string `json:"username"`
		NewPassword     string `json:"new_password"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, s.deps.Cfg.Security.MaxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || request.CurrentPassword == "" {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "current password required"))
		return
	}
	result, err := s.deps.Auth.ChangeCredentials(
		claims.Username(), request.CurrentPassword, request.Username, request.NewPassword,
		s.deps.Store.ReplaceAuthUser,
	)
	if errors.Is(err, store.ErrRevisionConflict) {
		writeAppErr(w, apperr.New(apperr.CodeConflict, 409, "account was updated elsewhere"))
		return
	}
	if errors.Is(err, store.ErrUsernameConflict) {
		writeAppErr(w, auth.ErrUsernameTaken)
		return
	}
	if err != nil {
		writeAppErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleChannels(w http.ResponseWriter, r *http.Request) {
	c := claimsFrom(r)
	p := principalFrom(r)
	base := s.deps.Catalog.PublicBase()
	all, err := s.deps.Catalog.ListViews(base, false, false)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if p.Role == "admin" || len(c.ChannelIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"channels": all})
		return
	}
	allow := map[string]struct{}{}
	for _, id := range c.ChannelIDs {
		allow[id] = struct{}{}
	}
	filtered := make([]catalog.ChannelView, 0, len(all))
	for _, ch := range all {
		if _, ok := allow[ch.ID]; ok {
			filtered = append(filtered, ch)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": filtered})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snapshot := s.deps.Observe.Snapshot()
	claims := claimsFrom(r)
	if principalFrom(r).Role != "admin" && len(claims.ChannelIDs) > 0 {
		allowed := make(map[string]struct{}, len(claims.ChannelIDs))
		for _, id := range claims.ChannelIDs {
			allowed[id] = struct{}{}
		}
		filtered := make([]observe.SessionStat, 0, len(snapshot.Sessions))
		for _, session := range snapshot.Sessions {
			if _, ok := allowed[session.ChannelID]; ok {
				filtered = append(filtered, session)
			}
		}
		snapshot.Sessions = filtered
		snapshot.SessionCount = len(filtered)
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.deps.Cfg.Observe.Enabled {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.deps.Observe.WritePrometheus(w); err != nil {
		s.deps.Log.Error("write metrics failed", "err", err)
	}
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	tok := extractPlayToken(r)
	chs, err := s.deps.Catalog.List(false)
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	c := claimsFrom(r)
	if c.Role != "admin" && len(c.ChannelIDs) > 0 {
		chs = s.deps.Catalog.FilterByIDs(chs, c.ChannelIDs)
	}
	base := s.deps.Catalog.PublicBase()
	epgURL := ""
	if s.epgActive() {
		epgURL = epgPublicURL(base)
	}
	body := s.deps.Catalog.M3U(chs, base, "/v1/play/", tok, epgURL)
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(body))
	s.deps.Observe.AddBytesOut(int64(len(body)))
}

func (s *Server) handlePlayIndex(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.PathValue("token") == "" {
		if err := s.authorizeChannel(r, id); err != nil {
			writeAppErr(w, err)
			return
		}
	}
	sess, err := s.deps.Sessions.Acquire(id)
	if err != nil {
		s.deps.Observe.IncError()
		writeAppErr(w, err)
		return
	}
	s.deps.Sessions.Touch(id)
	_, _, _, mode := sess.SourceSnapshot()
	switch mode {
	case "hls":
		s.serveHLSIndex(w, r, sess)
	case "dash":
		s.serveDashIndex(w, r, sess)
	default:
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "unsupported ingress"))
	}
}

func (s *Server) serveHLSIndex(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	channel, sourceURL, _, _ := sess.SourceSnapshot()
	headers := s.deps.Sessions.HeadersFor(channel)
	body, finalURL, err := s.deps.Sessions.Pull().GetBytes(r.Context(), pull.Request{
		URL:       mergeHLSDeliveryDirectives(sourceURL, r.URL.Query()),
		UserAgent: version.UserAgent(channel.UserAgent),
		Headers:   headers,
		ChannelID: channel.ID,
	})
	if err != nil {
		s.deps.Observe.IncError()
		writeAppErr(w, err)
		return
	}
	token := extractPlayToken(r)
	prefix := "/v1/play/" + channel.ID + "/u/"
	if t := r.PathValue("token"); t != "" && accesstoken.Valid(t) {
		prefix = "/p/" + t + "/play/" + channel.ID + "/u/"
		token = ""
	}
	chID := channel.ID
	rewritten, err := egress.RewritePlaylist(string(body), finalURL, prefix, s.deps.Allowed, s.shouldRewrite(chID))
	if err != nil {
		s.deps.Observe.IncError()
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if token != "" {
		rewritten = appendTokenToPlaylistURLs(rewritten, token)
	}
	s.deps.Observe.AddBytesOut(int64(len(rewritten)))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(rewritten))
}

func (s *Server) shouldRewrite(channelID string) egress.RewriteDecision {
	return func(abs string) bool {
		if s.deps.Egress == nil {
			return true
		}
		return s.deps.Egress.ShouldRewriteURL(abs, channelID)
	}
}

func (s *Server) serveDashIndex(w http.ResponseWriter, r *http.Request, sess *session.Session) {
	pub, generation := sess.PublicationSnapshot()
	if pub == nil {
		s.deps.Observe.IncError()
		writeAppErr(w, apperr.New(apperr.CodeNotReady, 502, "playlist not ready"))
		return
	}
	body, ok := pub.Playlist(pub.Master())
	if !ok {
		s.deps.Observe.IncError()
		writeAppErr(w, apperr.New(apperr.CodeNotReady, 502, "playlist not ready"))
		return
	}
	s.writePlaylist(w, r, sess, body, generation)
}

// writePlaylist serves a published playlist with every reference rewritten to
// this server. Playlists are a moving window, so they stay uncacheable.
func (s *Server) writePlaylist(w http.ResponseWriter, r *http.Request, sess *session.Session, body []byte, generation string) {
	channel, _, _, _ := sess.SourceSnapshot()
	out := rewriteLocalPlaylist(body, playLivePrefix(r, channel.ID), extractPlayToken(r), generation)
	s.deps.Observe.AddBytesOut(int64(len(out)))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(out)
}

func playLivePrefix(r *http.Request, channelID string) string {
	if t := r.PathValue("token"); t != "" && accesstoken.Valid(t) {
		return "/p/" + t + "/play/" + channelID + "/live/"
	}
	return "/v1/play/" + channelID + "/live/"
}

// handlePlayLiveFile serves published playlists and media assets. It never
// starts a session: a late segment request used to be able to bring a whole
// channel back up, which meant an idle-stopped channel could be revived by a
// player that had not even re-read the playlist. Only the playlist acquires.
func (s *Server) handlePlayLiveFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	file := path.Base(r.PathValue("file"))
	if !safeFileName(file) {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	if r.PathValue("token") == "" {
		if err := s.authorizeChannel(r, id); err != nil {
			writeAppErr(w, err)
			return
		}
	}
	sess, ok := s.deps.Sessions.Get(id)
	if !ok {
		// Gone, not Not Found: the player should go back to the playlist and
		// renegotiate, which is what restarts an on-demand channel.
		writeAppErr(w, apperr.New(apperr.CodeNotFound, 410, "session is not running"))
		return
	}
	pub, generation := sess.PublicationSnapshot()
	requested := r.URL.Query().Get("g")
	if generation != "" && (requested == "" || requested != generation && strings.HasSuffix(file, ".m3u8")) {
		redirectToPublicationGeneration(w, r, generation)
		return
	}
	if requested != "" && requested != generation {
		w.Header().Set("Retry-After", "1")
		writeAppErr(w, apperr.New(apperr.CodeNotFound, http.StatusGone, "publication generation is gone"))
		return
	}
	s.deps.Sessions.Touch(id)
	if pub == nil {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}

	if strings.HasSuffix(file, ".m3u8") {
		body, ok, err := playlistForRequest(r, pub, file)
		if err != nil {
			writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, err.Error()))
			return
		}
		if !ok {
			writeAppErr(w, apperr.ErrNotFound)
			return
		}
		s.writePlaylist(w, r, sess, body, generation)
		return
	}

	asset, ok := pub.Asset(file)
	if !ok {
		if contextual, supportsContext := pub.(packager.ContextPublication); supportsContext {
			var err error
			waitCtx, cancel := context.WithTimeout(r.Context(), llhlsWaitTimeout)
			asset, ok, err = contextual.AssetContext(waitCtx, file)
			cancel()
			if err != nil {
				if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
					return
				}
				if errors.Is(err, context.DeadlineExceeded) {
					s.deps.Observe.IncError()
					w.Header().Set("Retry-After", "1")
					writeAppErr(w, apperr.New(apperr.CodeUnavailable, http.StatusServiceUnavailable, "media part not ready"))
					return
				}
				writeAppErr(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, err.Error()))
				return
			}
		}
	}
	if !ok {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	f, err := os.Open(asset.Path)
	if err != nil {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", contentTypeFor(file))
	setAssetCacheHeaders(w, asset.Immutable)
	counter := &bodyCountWriter{ResponseWriter: w}
	http.ServeContent(counter, r, file, asset.ModTime, f)
	s.deps.Observe.AddBytesOut(counter.written)
}

type bodyCountWriter struct {
	http.ResponseWriter
	written int64
}

func (w *bodyCountWriter) Write(body []byte) (int, error) {
	n, err := w.ResponseWriter.Write(body)
	w.written += int64(n)
	return n, err
}

func redirectToPublicationGeneration(w http.ResponseWriter, r *http.Request, generation string) {
	target := *r.URL
	query := target.Query()
	query.Set("g", generation)
	target.RawQuery = query.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target.String(), http.StatusTemporaryRedirect)
}

func playlistForRequest(r *http.Request, publication packager.Publication, name string) ([]byte, bool, error) {
	request, lowLatency, err := parseHLSPlaylistRequest(r)
	if err != nil {
		return nil, false, err
	}
	contextual, supportsContext := publication.(packager.ContextPublication)
	if !lowLatency || !supportsContext {
		body, ok := publication.Playlist(name)
		return body, ok, nil
	}
	waitCtx, cancel := context.WithTimeout(r.Context(), llhlsWaitTimeout)
	view, ok, err := contextual.PlaylistContext(waitCtx, name, request)
	cancel()
	if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
		body, found := publication.Playlist(name)
		return body, found, nil
	}
	if err != nil {
		return nil, ok, err
	}
	return view.Body, ok, nil
}

func parseHLSPlaylistRequest(r *http.Request) (packager.PlaylistRequest, bool, error) {
	query := r.URL.Query()
	request := packager.PlaylistRequest{}
	lowLatency := false
	if raw, present := query["_HLS_skip"]; present {
		lowLatency = true
		value := ""
		if len(raw) > 0 {
			value = strings.ToUpper(strings.TrimSpace(raw[0]))
		}
		if value != "YES" && value != "V2" {
			return request, true, fmt.Errorf("_HLS_skip must be YES or v2")
		}
		request.Skip = true
	}
	parseUint := func(key string) (*uint64, error) {
		raw, present := query[key]
		if !present {
			return nil, nil
		}
		lowLatency = true
		if len(raw) == 0 || strings.TrimSpace(raw[0]) == "" {
			return nil, fmt.Errorf("%s requires an unsigned integer", key)
		}
		value, parseErr := strconv.ParseUint(raw[0], 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("%s requires an unsigned integer", key)
		}
		return &value, nil
	}
	var err error
	request.MSN, err = parseUint("_HLS_msn")
	if err != nil {
		return request, lowLatency, err
	}
	request.Part, err = parseUint("_HLS_part")
	if err != nil {
		return request, lowLatency, err
	}
	if request.Part != nil && request.MSN == nil {
		return request, lowLatency, fmt.Errorf("_HLS_part requires _HLS_msn")
	}
	return request, lowLatency, nil
}

// setAssetCacheHeaders overrides the global no-store for published media.
// Without this, a hundred players mean a hundred full re-reads and the shared
// upstream fetch buys nothing at the HTTP layer. The exemption is opt-in per
// route so it never widens the caching of admin or status responses.
//
// private, not public: these URLs can carry an access token, and a shared cache
// holding them would widen where that token is exposed.
func setAssetCacheHeaders(w http.ResponseWriter, immutable bool) {
	w.Header().Del("Pragma")
	if !immutable {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
}

func (s *Server) handlePlayUpstream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	escaped := r.PathValue("upstream")
	abs, err := egress.DecodeUpstream(escaped)
	if err != nil {
		writeAppErr(w, apperr.New(apperr.CodeInvalid, 400, "bad upstream"))
		return
	}
	if err := security.MediaHostOK(abs, s.deps.Allowed); err != nil {
		writeAppErr(w, apperr.New(apperr.CodeForbidden, 403, "upstream host not allowed"))
		return
	}
	abs = mergeHLSDeliveryDirectives(abs, r.URL.Query())
	if r.PathValue("token") == "" {
		if err := s.authorizeChannel(r, id); err != nil {
			writeAppErr(w, err)
			return
		}
	}
	ch, ok := s.deps.Catalog.Get(id)
	if !ok {
		writeAppErr(w, session.ErrNotFound)
		return
	}
	if _, err := s.deps.Sessions.Acquire(id); err != nil {
		writeAppErr(w, err)
		return
	}
	s.deps.Sessions.Touch(id)
	headers := s.deps.Sessions.HeadersFor(ch)
	res, err := s.deps.Sessions.Pull().Get(r.Context(), pull.Request{
		URL:       abs,
		UserAgent: version.UserAgent(ch.UserAgent),
		Headers:   headers,
		ChannelID: id,
	})
	if err != nil {
		s.deps.Observe.IncError()
		writeAppErr(w, err)
		return
	}
	defer res.Body.Close()

	ct := res.ContentType
	if ct == "" {
		ct = contentTypeFor(abs)
	}
	if strings.Contains(ct, "mpegurl") || strings.HasSuffix(strings.Split(abs, "?")[0], ".m3u8") {
		b, err := io.ReadAll(io.LimitReader(res.Body, s.deps.Cfg.Security.MaxPlaylistBytes+1))
		if err != nil {
			writeAppErr(w, apperr.Wrap(apperr.CodeUpstream, 502, "read playlist failed", err))
			return
		}
		if int64(len(b)) > s.deps.Cfg.Security.MaxPlaylistBytes {
			writeAppErr(w, apperr.New(apperr.CodeUpstream, 502, "playlist too large"))
			return
		}
		token := extractPlayToken(r)
		prefix := "/v1/play/" + id + "/u/"
		if t := r.PathValue("token"); t != "" && accesstoken.Valid(t) {
			prefix = "/p/" + t + "/play/" + id + "/u/"
			token = ""
		}
		out, err := egress.RewritePlaylist(string(b), res.FinalURL, prefix, s.deps.Allowed, s.shouldRewrite(id))
		if err != nil {
			writeAppErr(w, apperr.Internal(err))
			return
		}
		if token != "" {
			out = appendTokenToPlaylistURLs(out, token)
		}
		s.deps.Observe.AddBytesOut(int64(len(out)))
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(out))
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	stream := &commitTrackingWriter{ResponseWriter: w}
	n, copyErr := io.Copy(stream, res.Body)
	s.deps.Observe.AddBytesOut(n)
	if copyErr != nil {
		s.deps.Observe.IncError()
		s.deps.Log.Warn("stream upstream body failed", "channel", id, "err", copyErr)
		if !stream.committed {
			writeAppErr(w, apperr.Wrap(apperr.CodeUpstream, http.StatusBadGateway, "read upstream body failed", copyErr))
			return
		}
		panic(http.ErrAbortHandler)
	}
}

type commitTrackingWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *commitTrackingWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	if n > 0 {
		w.committed = true
	}
	return n, err
}

func mergeHLSDeliveryDirectives(raw string, requestQuery url.Values) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	for _, key := range []string{"_HLS_msn", "_HLS_part", "_HLS_skip"} {
		if value := requestQuery.Get(key); value != "" {
			query.Set(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

type ctxKey int

const (
	claimsKey    ctxKey = 1
	principalKey ctxKey = 2
)

type requestPrincipal struct {
	Kind    string
	Subject string
	Role    string
	TokenID string
	Prefix  string
	Scopes  []string
}

func principalFrom(r *http.Request) requestPrincipal {
	p, _ := r.Context().Value(principalKey).(requestPrincipal)
	return p
}

func claimsFrom(r *http.Request) auth.Claims {
	c, _ := r.Context().Value(claimsKey).(auth.Claims)
	return c
}

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := auth.BearerToken(r.Header.Get("Authorization"))
		if admintoken.Valid(tok) {
			s.requireAdminToken(w, r, tok, next)
			return
		}
		c, err := s.deps.Auth.Parse(tok)
		if err != nil {
			writeAppErr(w, err)
			return
		}
		if c.Role == "preview" {
			writeAppErr(w, auth.ErrInvalidToken)
			return
		}
		principal := requestPrincipal{Kind: "session", Subject: c.Username(), Role: c.Role}
		ctx := context.WithValue(r.Context(), claimsKey, c)
		ctx = context.WithValue(ctx, principalKey, principal)
		next(w, r.WithContext(ctx))
	}
}

var adminTokenRouteScopes = map[string]admintoken.Scope{
	"GET /v1/me":                               admintoken.ScopeRead,
	"GET /v1/channels":                         admintoken.ScopeRead,
	"GET /v1/status":                           admintoken.ScopeRead,
	"GET /v1/admin/channels":                   admintoken.ScopeRead,
	"GET /v1/admin/channels/{id}":              admintoken.ScopeRead,
	"POST /v1/admin/channels":                  admintoken.ScopeWrite,
	"PUT /v1/admin/channels/{id}":              admintoken.ScopeWrite,
	"DELETE /v1/admin/channels/{id}":           admintoken.ScopeDelete,
	"POST /v1/admin/channels/enable-all":       admintoken.ScopeWrite,
	"POST /v1/admin/channels/disable-all":      admintoken.ScopeWrite,
	"GET /v1/admin/epg/presets":                admintoken.ScopeRead,
	"GET /v1/admin/epg/sources":                admintoken.ScopeRead,
	"POST /v1/admin/epg/sources":               admintoken.ScopeWrite,
	"PUT /v1/admin/epg/sources/{id}":           admintoken.ScopeWrite,
	"DELETE /v1/admin/epg/sources/{id}":        admintoken.ScopeDelete,
	"GET /v1/admin/epg/matches":                admintoken.ScopeRead,
	"POST /v1/admin/epg/refresh":               admintoken.ScopeRefresh,
	"GET /v1/admin/upstreams":                  admintoken.ScopeRead,
	"GET /v1/admin/access-tokens":              admintoken.ScopeRead,
	"POST /v1/admin/access-tokens":             admintoken.ScopeWrite,
	"POST /v1/admin/access-tokens/{id}/revoke": admintoken.ScopeDelete,
	"DELETE /v1/admin/access-tokens/{id}":      admintoken.ScopeDelete,
	"GET /v1/admin/settings":                   admintoken.ScopeRead,
	"PUT /v1/admin/settings":                   admintoken.ScopeWrite,
	"POST /v1/admin/channels/{id}/probe":       admintoken.ScopeRefresh,
	"POST /v1/admin/source-probes":             admintoken.ScopeRefresh,
	"POST /v1/admin/channels/{id}/warmup":      admintoken.ScopeRefresh,
	"POST /v1/admin/channels/{id}/preview":     admintoken.ScopeRefresh,
	"DELETE /v1/admin/sessions/{id}":           admintoken.ScopeRefresh,
	"PUT /v1/admin/channels/reorder":           admintoken.ScopeWrite,
	"POST /v1/admin/import/m3u":                admintoken.ScopeWrite,
	// Exporting an M3U creates a new playback credential, so this is a write
	// even though the response itself is a read-only playlist.
	"POST /v1/admin/exports/m3u":           admintoken.ScopeWrite,
	"GET /v1/admin/access-logs":            admintoken.ScopeRead,
	"DELETE /v1/admin/access-logs":         admintoken.ScopeDelete,
	"GET /v1/admin/egress":                 admintoken.ScopeRead,
	"PUT /v1/admin/egress":                 admintoken.ScopeWrite,
	"POST /v1/admin/egress/proxies":        admintoken.ScopeWrite,
	"PUT /v1/admin/egress/proxies/{id}":    admintoken.ScopeWrite,
	"DELETE /v1/admin/egress/proxies/{id}": admintoken.ScopeDelete,
	"POST /v1/admin/egress/rules":          admintoken.ScopeWrite,
	"PUT /v1/admin/egress/rules/{id}":      admintoken.ScopeWrite,
	"DELETE /v1/admin/egress/rules/{id}":   admintoken.ScopeDelete,
	"POST /v1/admin/egress/test":           admintoken.ScopeRefresh,
}

var sessionOnlyAdminRoutes = map[string]struct{}{
	"PUT /v1/me/credentials":                {},
	"GET /v1/admin/api-tokens":              {},
	"POST /v1/admin/api-tokens":             {},
	"PUT /v1/admin/api-tokens/{id}":         {},
	"POST /v1/admin/api-tokens/{id}/rotate": {},
	"POST /v1/admin/api-tokens/{id}/revoke": {},
	"DELETE /v1/admin/api-tokens/{id}":      {},
	"GET /v1/admin/api-token-logs":          {},
}

func (s *Server) requireAdminToken(w http.ResponseWriter, r *http.Request, plain string, next http.HandlerFunc) {
	if s.deps.Store == nil {
		writeAppErr(w, auth.ErrInvalidToken)
		return
	}
	row, found, err := s.deps.Store.GetAdminAPITokenByHash(admintoken.Hash(plain))
	if err != nil {
		writeAppErr(w, apperr.Internal(err))
		return
	}
	if !found {
		writeAppErr(w, auth.ErrInvalidToken)
		return
	}
	required, registered := adminTokenRouteScopes[r.Pattern]
	status := http.StatusOK
	decision := "allow"
	reason := ""
	switch {
	case !row.Enabled || row.RevokedAt > 0:
		status, decision, reason = http.StatusUnauthorized, "deny", "revoked"
	case row.ExpiresAt > 0 && row.ExpiresAt <= time.Now().Unix():
		status, decision, reason = http.StatusUnauthorized, "deny", "expired"
	case hasSessionOnlyRoute(r.Pattern):
		status, decision, reason = http.StatusForbidden, "deny", "session_required"
	case !registered:
		status, decision, reason = http.StatusForbidden, "deny", "route_not_available"
	case !admintoken.Allows(admintoken.DecodeScopes(row.ScopeJSON), required):
		status, decision, reason = http.StatusForbidden, "deny", "missing_scope"
	}
	if decision == "deny" {
		s.recordAdminTokenLog(r, row, string(required), decision, reason, status)
		if status == http.StatusUnauthorized {
			writeAppErr(w, auth.ErrInvalidToken)
		} else {
			writeAppErr(w, apperr.New(apperr.CodeForbidden, status, "API token permission denied"))
		}
		return
	}
	_ = s.deps.Store.TouchAdminAPIToken(row.ID)
	principal := requestPrincipal{
		Kind: "api_token", Subject: row.Name, Role: "admin", TokenID: row.ID,
		Prefix: row.Prefix, Scopes: admintoken.DecodeScopes(row.ScopeJSON),
	}
	ctx := context.WithValue(r.Context(), principalKey, principal)
	next(w, r.WithContext(ctx))
	if sw, ok := w.(*statusWriter); ok {
		status = sw.code
	}
	s.recordAdminTokenLog(r, row, string(required), decision, reason, status)
}

func hasSessionOnlyRoute(pattern string) bool {
	_, ok := sessionOnlyAdminRoutes[pattern]
	return ok
}

func (s *Server) recordAdminTokenLog(r *http.Request, token store.AdminAPITokenRow, scope, decision, reason string, status int) {
	if s.deps.Store == nil {
		return
	}
	_ = s.deps.Store.InsertAdminAPITokenLog(store.AdminAPITokenLogRow{
		TokenID: token.ID, TokenPrefix: token.Prefix, Method: r.Method, Path: r.URL.Path,
		Scope: scope, Decision: decision, Reason: reason, Status: status, Remote: clientIP(r),
		UserAgent: r.UserAgent(), RequestID: r.Header.Get("X-Request-ID"),
	})
}

func (s *Server) requirePlayAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.deps.Cfg.Security.PlayRequireAuth {
			next(w, r)
			return
		}
		c, err := s.deps.Auth.Parse(extractPlayToken(r))
		if err != nil {
			writeAppErr(w, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), claimsKey, c)))
	}
}

func (s *Server) authorizeChannel(r *http.Request, channelID string) error {
	if !s.deps.Cfg.Security.PlayRequireAuth {
		return nil
	}
	c := claimsFrom(r)
	if c.Username() == "" {
		return auth.ErrInvalidToken
	}
	if !s.deps.Auth.CanAccessChannel(c, channelID) {
		return auth.ErrForbiddenChannel
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAppErr(w http.ResponseWriter, err error) {
	code, status, msg := apperr.PublicMessage(err)
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    string(code),
			"message": msg,
		},
	})
}

func contentTypeFor(name string) string {
	n := strings.ToLower(strings.Split(name, "?")[0])
	switch {
	case strings.HasSuffix(n, ".m3u8"):
		return "application/vnd.apple.mpegurl"
	case strings.HasSuffix(n, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(n, ".m4s"), strings.HasSuffix(n, ".mp4"):
		return "video/mp4"
	case strings.HasSuffix(n, ".aac"):
		return "audio/aac"
	case strings.HasSuffix(n, ".vtt"):
		return "text/vtt"
	default:
		return "application/octet-stream"
	}
}

func appendTokenToPlaylistURLs(playlist, token string) string {
	lines := strings.Split(playlist, "\n")
	for i, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == "" {
			continue
		}
		if strings.HasPrefix(trim, "#") {
			if strings.Contains(trim, `URI="`) {
				lines[i] = injectTokenInURITag(trim, token)
			}
			continue
		}
		if strings.HasPrefix(trim, "http://") || strings.HasPrefix(trim, "https://") || strings.HasPrefix(trim, "/") {
			lines[i] = appendQuery(trim, "token", token)
		}
	}
	return strings.Join(lines, "\n")
}

func injectTokenInURITag(tag, token string) string {
	const key = `URI="`
	idx := strings.Index(tag, key)
	if idx < 0 {
		return tag
	}
	start := idx + len(key)
	end := strings.Index(tag[start:], `"`)
	if end < 0 {
		return tag
	}
	uri := tag[start : start+end]
	if strings.HasPrefix(uri, "http://") || strings.HasPrefix(uri, "https://") || strings.HasPrefix(uri, "/") {
		uri = appendQuery(uri, "token", token)
	}
	return tag[:start] + uri + tag[start+end:]
}

func appendQuery(raw, k, v string) string {
	if strings.Contains(raw, k+"=") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		return raw + sep + k + "=" + url.QueryEscape(v)
	}
	q := u.Query()
	q.Set(k, v)
	u.RawQuery = q.Encode()
	return u.String()
}

func safeFileName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") {
		return false
	}
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func redactRequestPath(raw string) string {
	if !strings.HasPrefix(raw, "/p/") {
		return raw
	}
	rest := strings.TrimPrefix(raw, "/p/")
	token, suffix, ok := strings.Cut(rest, "/")
	if !ok || token == "" {
		return "/p/[redacted]"
	}
	prefix := accesstoken.Prefix(token)
	if prefix == "" {
		prefix = "[redacted]"
	}
	return "/p/" + prefix + "…/" + suffix
}

func randomID() string {
	return strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")
}
