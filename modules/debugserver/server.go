package debugserver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/babywbx/kiln/modules/config"
)

type Server struct {
	http *http.Server
	log  *slog.Logger
}

func New(cfg config.Pprof, log *slog.Logger) (*Server, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return nil, fmt.Errorf("parse pprof listener: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("pprof listener must use a loopback IP")
	}
	if log == nil {
		log = slog.Default()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	for _, profile := range []string{
		"allocs", "block", "goroutine", "heap", "mutex", "threadcreate",
	} {
		mux.Handle("GET /debug/pprof/"+profile, pprof.Handler(profile))
	}

	return &Server{
		http: &http.Server{
			Addr:              cfg.Listen,
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		},
		log: log,
	}, nil
}

func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

func (s *Server) Start() error {
	s.log.Info("pprof listening", "addr", s.http.Addr)
	return s.http.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}
