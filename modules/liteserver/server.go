package liteserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/apperr"
	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/playback"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/security"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/staticcatalog"
	"github.com/babywbx/kiln/modules/version"
)

type Server struct {
	log  *slog.Logger
	mux  *http.ServeMux
	http *http.Server

	cfg      config.File
	catalog  *staticcatalog.Service
	sessions *session.Manager
	observe  *observe.Service
	playback *playback.Handler
	cancel   context.CancelFunc
	auth     *auth.Service
	login    *security.Limiter
}

type requestContextKey int

const claimsContextKey requestContextKey = 1

func New(cfg config.File, log *slog.Logger) (*Server, error) {
	if log == nil {
		log = slog.Default()
	}
	router, err := proxyegress.NewRouter(proxyegress.ConfigFromFile(cfg))
	if err != nil {
		return nil, err
	}
	metrics := observe.New()
	allowed := cfg.AllowedHostSet()
	allowedPrivate := cfg.ExplicitAllowedHostSet()
	catalog := staticcatalog.New(cfg)
	puller := pull.New(pull.Options{
		Observe: metrics, Allowed: allowedPrivate, MaxPlaylist: cfg.Security.MaxPlaylistBytes, Router: router,
		StallTimeout: time.Duration(cfg.Packager.FetchStallSec) * time.Second,
	})
	sessions := session.NewNativeManager(
		catalog, puller, metrics, cfg.Server.DataDir, cfg.FFmpeg, cfg.GlobalKeys(), log, router,
	)
	authService, err := auth.New(cfg.Auth, cfg.TokenTTL(), auth.Options{DataDir: cfg.Server.DataDir})
	if err != nil {
		return nil, err
	}
	lifecycle, cancel := context.WithCancel(context.Background())
	sessions.Start(lifecycle)
	mux := http.NewServeMux()
	server := &Server{
		log: log, mux: mux, cfg: cfg, catalog: catalog, sessions: sessions,
		observe: metrics, cancel: cancel,
		auth: authService, login: security.NewLimiter(cfg.Auth.LoginRatePerMin),
	}
	server.playback = playback.New(playback.Deps{
		Cfg: cfg, Catalog: catalog, Sessions: sessions, Observe: metrics,
		Egress: router, Log: log, Allowed: allowed,
		Authorize: server.authorizeChannel, Token: playToken,
	})
	mux.HandleFunc("GET /healthz", server.handleHealth)
	mux.HandleFunc("GET /readyz", server.handleReady)
	mux.HandleFunc("POST /v1/auth/login", server.handleLogin)
	mux.HandleFunc("GET /v1/playlist.m3u", server.requirePlayAuth(server.handlePlaylist))
	mux.HandleFunc("GET /v1/play/{id}/index.m3u8", server.requirePlayAuth(server.playback.HandleIndex))
	mux.HandleFunc("GET /v1/play/{id}/live/{file}", server.requirePlayAuth(server.playback.HandleLiveFile))
	mux.HandleFunc("GET /v1/play/{id}/u/{upstream}", server.requirePlayAuth(server.playback.HandleUpstream))
	server.http = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           server.withMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeout) * time.Second,
		IdleTimeout:       time.Duration(cfg.Server.IdleTimeout) * time.Second,
	}
	if cfg.Server.WriteTimeout > 0 {
		server.http.WriteTimeout = time.Duration(cfg.Server.WriteTimeout) * time.Second
	}
	return server, nil
}

func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

func (s *Server) Start() error {
	s.log.Info("listening", "addr", s.http.Addr, "version", version.Version, "variant", "lite")
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.cancel()
	httpErr := s.http.Shutdown(ctx)
	sessionErr := s.sessions.ShutdownContext(ctx)
	return errors.Join(httpErr, sessionErr)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.login.Allow(security.ClientIP(r)) {
		writeAppError(w, apperr.ErrTooMany)
		return
	}
	maxBody := s.cfg.Security.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || strings.TrimSpace(request.Username) == "" || request.Password == "" {
		writeAppError(w, apperr.New(apperr.CodeInvalid, http.StatusBadRequest, "username and password required"))
		return
	}
	result, err := s.auth.Login(strings.TrimSpace(request.Username), request.Password)
	if err != nil {
		s.observe.IncError()
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		security.ApplyCORS(w, r, s.cfg.Security.CORSOrigins)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !security.IsLocalHealthRequest(r) && !security.RequestHostAllowed(r, s.cfg.Security.PublicHosts) {
			writeAppError(w, apperr.New(apperr.CodeForbidden, http.StatusForbidden, "host not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	channels := s.catalog.List()
	if s.cfg.Security.PlayAuthRequired() {
		claims, ok := r.Context().Value(claimsContextKey).(auth.Claims)
		if !ok {
			writeAppError(w, auth.ErrInvalidToken)
			return
		}
		if claims.Role != "admin" {
			channels = s.catalog.FilterByIDs(channels, claims.ChannelIDs)
		}
	}
	body := s.catalog.Playlist(channels, playToken(r))
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, body)
	s.observe.AddBytesOut(int64(len(body)))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func writeAppError(w http.ResponseWriter, err error) {
	code, status, message := apperr.PublicMessage(err)
	writeError(w, status, string(code), message)
}

func (s *Server) requirePlayAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Security.PlayAuthRequired() {
			next(w, r)
			return
		}
		claims, err := s.auth.Parse(playToken(r))
		if err != nil {
			writeAppError(w, err)
			return
		}
		ctx := context.WithValue(r.Context(), claimsContextKey, claims)
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) authorizeChannel(r *http.Request, channelID string) error {
	if !s.cfg.Security.PlayAuthRequired() {
		return nil
	}
	claims, ok := r.Context().Value(claimsContextKey).(auth.Claims)
	if !ok || claims.Username() == "" {
		return auth.ErrInvalidToken
	}
	if !s.auth.CanAccessChannel(claims, channelID) {
		return auth.ErrForbiddenChannel
	}
	return nil
}

func playToken(r *http.Request) string {
	if token := auth.BearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	return r.URL.Query().Get("token")
}
