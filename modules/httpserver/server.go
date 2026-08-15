//go:build !lite

package httpserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/accesstoken"
	"github.com/babywbx/kiln/modules/admintoken"
	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/epg"
	"github.com/babywbx/kiln/modules/logging"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/playback"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/security"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
	"github.com/babywbx/kiln/modules/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
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
	Tracing  bool
}

type Server struct {
	deps   Deps
	mux    *http.ServeMux
	http   *http.Server
	tls    *http.Server
	loginL *security.Limiter
	play   *playback.Handler
	logo   *http.Client
}

func New(deps Deps) *Server {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	s := &Server{
		deps:   deps,
		mux:    http.NewServeMux(),
		loginL: security.NewLimiter(deps.Cfg.Auth.LoginRatePerMin),
		logo: &http.Client{
			Timeout: 15 * time.Second,
			Transport: proxyegress.NewPinnedTransport(
				http.DefaultTransport.(*http.Transport), deps.Egress, "",
				deps.Cfg.ExplicitAllowedHostSet(),
			),
		},
	}
	s.play = playback.New(playback.Deps{
		Cfg: deps.Cfg, Catalog: deps.Catalog, Sessions: deps.Sessions,
		Observe: deps.Observe, Egress: deps.Egress, Log: deps.Log,
		Allowed: deps.Allowed, Authorize: s.authorizeChannel, Token: extractPlayToken,
	})
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
	if splitAddr := strings.TrimSpace(deps.Cfg.Server.TLSListen); splitAddr != "" {
		s.tls = &http.Server{
			Addr:              splitAddr,
			Handler:           handler,
			ReadHeaderTimeout: s.http.ReadHeaderTimeout,
			ReadTimeout:       s.http.ReadTimeout,
			IdleTimeout:       s.http.IdleTimeout,
			WriteTimeout:      s.http.WriteTimeout,
		}
	}
	return s
}

func (s *Server) Handler() http.Handler { return s.http.Handler }

func (s *Server) Start() error {
	tlsEnabled, err := s.tlsEnabled()
	if err != nil {
		return err
	}
	if !tlsEnabled {
		s.deps.Log.Info("listening", "addr", s.deps.Cfg.Server.Listen, "scheme", "http", "version", version.Version)
		return s.http.ListenAndServe()
	}
	material, err := s.tlsMaterial()
	if err != nil {
		return err
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{material.Certificate}, MinVersion: tls.VersionTLS12}
	if s.tls == nil {
		s.http.TLSConfig = tlsConfig
		s.deps.Log.Info("listening",
			"addr", s.deps.Cfg.Server.Listen, "scheme", "https", "certificate", material.Source,
			"expires", material.NotAfter.Format(time.RFC3339), "version", version.Version)
		return s.http.ListenAndServeTLS("", "")
	}

	s.tls.TLSConfig = tlsConfig
	s.http.Handler = s.plaintextSurface(s.http.Handler, s.tls.Addr)
	s.deps.Log.Info("listening",
		"addr", s.tls.Addr, "scheme", "https", "certificate", material.Source,
		"expires", material.NotAfter.Format(time.RFC3339), "version", version.Version)
	s.deps.Log.Info("listening",
		"addr", s.deps.Cfg.Server.Listen, "scheme", "http", "surface", "playback", "version", version.Version)

	errs := make(chan error, 2)
	go func() { errs <- s.tls.ListenAndServeTLS("", "") }()
	go func() { errs <- s.http.ListenAndServe() }()
	return <-errs
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.tls == nil {
		return s.http.Shutdown(ctx)
	}
	errs := make(chan error, 2)
	go func() { errs <- s.http.Shutdown(ctx) }()
	go func() { errs <- s.tls.Shutdown(ctx) }()
	return errors.Join(<-errs, <-errs)
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
	s.mux.HandleFunc("GET /v1/playlist.m3u", s.requirePlayAuth(s.handlePlaylist))
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
		var span trace.Span
		if s.deps.Tracing {
			traceContext := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			traceContext, span = otel.Tracer("kiln/httpserver").Start(traceContext, "http.server")
			r = r.WithContext(traceContext)
			span.SetAttributes(attribute.String("http.request.method", r.Method))
		}
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
		security.ApplyCORS(ww, r, s.deps.Cfg.Security.CORSOrigins)

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
			if s.deps.Tracing {
				span.SetAttributes(attribute.Int("http.response.status_code", ww.code))
				if r.Pattern != "" {
					span.SetAttributes(attribute.String("http.route", r.Pattern))
				}
				if ww.code >= http.StatusInternalServerError {
					span.SetStatus(codes.Error, http.StatusText(ww.code))
				}
				span.End()
			}
			if s.deps.Log.Enabled(r.Context(), level) {
				s.deps.Log.Log(r.Context(), level, "request",
					"remote", security.ClientIP(r),
					"method", r.Method,
					"path", redactRequestPath(r.URL.Path),
					"status", ww.code,
					"dur_ms", time.Since(start).Milliseconds(),
					"request_id", reqID,
				)
			}
			if abortResponse {
				panic(http.ErrAbortHandler)
			}
		}()

		if r.Method == http.MethodOptions {
			ww.WriteHeader(http.StatusNoContent)
			return
		}
		if !s.requestHostAllowed(r) {
			writeAppErr(ww, apperr.New(apperr.CodeForbidden, 403, "host not allowed"))
			return
		}
		next.ServeHTTP(ww, r)
	})
}

func (s *Server) requestHostAllowed(r *http.Request) bool {
	return security.IsLocalHealthRequest(r) || security.RequestHostAllowed(r, s.deps.Cfg.Security.PublicHosts)
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
		if ch.Ingress == "dash" && s.deps.Cfg.EngineFor(ch) == config.EngineFFmpeg {
			needFF = true
			break
		}
	}
	if needFF {
		if s.deps.Sessions == nil || !s.deps.Sessions.FFmpegAvailable() {
			writeAppErr(w, apperr.New(apperr.CodeNotReady, 503, "ffmpeg compatibility engine is not available"))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type loginReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := security.ClientIP(r)
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
	if !s.loginL.Allow("credentials:" + claims.Username() + ":" + security.ClientIP(r)) {
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
	if !s.deps.Cfg.Observe.EnabledOrDefault() {
		writeAppErr(w, apperr.ErrNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.deps.Observe.WritePrometheus(w); err != nil {
		s.deps.Log.Error("write metrics failed", "err", err)
	}
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	tok := ""
	if s.deps.Cfg.Security.PlayAuthRequired() {
		tok = extractPlayToken(r)
	}
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
	s.play.HandleIndex(w, r)
}

func (s *Server) handlePlayLiveFile(w http.ResponseWriter, r *http.Request) {
	s.play.HandleLiveFile(w, r)
}

func (s *Server) handlePlayUpstream(w http.ResponseWriter, r *http.Request) {
	s.play.HandleUpstream(w, r)
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
	"POST /v1/admin/exports/m3u":               admintoken.ScopeWrite,
	"GET /v1/admin/access-logs":                admintoken.ScopeRead,
	"DELETE /v1/admin/access-logs":             admintoken.ScopeDelete,
	"GET /v1/admin/egress":                     admintoken.ScopeRead,
	"PUT /v1/admin/egress":                     admintoken.ScopeWrite,
	"POST /v1/admin/egress/proxies":            admintoken.ScopeWrite,
	"PUT /v1/admin/egress/proxies/{id}":        admintoken.ScopeWrite,
	"DELETE /v1/admin/egress/proxies/{id}":     admintoken.ScopeDelete,
	"POST /v1/admin/egress/rules":              admintoken.ScopeWrite,
	"PUT /v1/admin/egress/rules/{id}":          admintoken.ScopeWrite,
	"DELETE /v1/admin/egress/rules/{id}":       admintoken.ScopeDelete,
	"POST /v1/admin/egress/test":               admintoken.ScopeRefresh,
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
		TokenID: token.ID, TokenPrefix: token.Prefix, Method: r.Method, Path: redactRequestPath(r.URL.Path),
		Scope: scope, Decision: decision, Reason: reason, Status: status, Remote: security.ClientIP(r),
		UserAgent: r.UserAgent(), RequestID: r.Header.Get("X-Request-ID"),
	})
}

func (s *Server) requirePlayAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.deps.Cfg.Security.PlayAuthRequired() {
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
	if !s.deps.Cfg.Security.PlayAuthRequired() {
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

func redactRequestPath(raw string) string {
	if strings.HasPrefix(raw, "/p/") {
		rest := strings.TrimPrefix(raw, "/p/")
		token, suffix, ok := strings.Cut(rest, "/")
		if !ok || token == "" {
			return "/p/[redacted]"
		}
		prefix := accesstoken.Prefix(token)
		if prefix == "" {
			prefix = "[redacted]"
		}
		raw = "/p/" + prefix + "…/" + suffix
	}
	if prefix, _, ok := strings.Cut(raw, "/u/"); ok {
		return prefix + "/u/[redacted]"
	}
	return raw
}

func randomID() string {
	return strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")
}
