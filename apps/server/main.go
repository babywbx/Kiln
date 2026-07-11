package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/logging"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
	"github.com/babywbx/kiln/modules/version"
)

func main() {
	cfgPath := flag.String("config", "configs/examples/kiln.toml", "path to kiln.toml or kiln.jsonc")
	flag.Parse()

	boot := logging.Bootstrap()
	slog.SetDefault(boot)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		boot.Error("config load failed", "err", err, "config", *cfgPath)
		os.Exit(1)
	}
	log := logging.NewWith(logging.Options{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
		Color:  cfg.Logging.Color,
	})
	slog.SetDefault(log)

	if err := os.MkdirAll(cfg.Server.DataDir, 0o750); err != nil {
		log.Error("create data dir failed", "err", err, "path", cfg.Server.DataDir)
		os.Exit(1)
	}

	db, err := store.Open(cfg.Server.DataDir)
	if err != nil {
		log.Error("sqlite open failed", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.SeedFromConfig(cfg); err != nil {
		log.Error("sqlite seed failed", "err", err)
		os.Exit(1)
	}

	egCfg, err := proxyegress.ConfigFromStore(db, cfg)
	if err != nil {
		log.Error("egress config failed", "err", err)
		os.Exit(1)
	}
	egressRouter, err := proxyegress.NewRouter(egCfg)
	if err != nil {
		log.Error("egress router failed", "err", err)
		os.Exit(1)
	}

	obs := observe.New()
	allowed := cfg.AllowedHostSet()
	authSvc, err := auth.New(cfg.Auth, cfg.TokenTTL(), auth.Options{DataDir: cfg.Server.DataDir})
	if err != nil {
		log.Error("auth init failed", "err", err)
		os.Exit(1)
	}
	cat := catalog.New(cfg, db)
	puller := pull.New(pull.Options{
		Observe:     obs,
		Allowed:     allowed,
		MaxPlaylist: cfg.Security.MaxPlaylistBytes,
		Router:      egressRouter,
	})
	sessions := session.NewManager(cat, puller, obs, cfg.Server.DataDir, cfg.FFmpeg, log, egressRouter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions.Start(ctx)

	srv := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Auth:     authSvc,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Store:    db,
		Egress:   egressRouter,
		Log:      log,
		Allowed:  allowed,
	})

	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "err", err)
			cancel()
		}
	}()

	chs, _ := cat.List(false)
	log.Info("kiln starting",
		"version", version.Version,
		"config", abs(*cfgPath),
		"listen", cfg.Server.Listen,
		"channels", len(chs),
		"proxies", len(cfg.Proxies),
		"egress_default", cfg.Egress.Default,
		"playlist_policy", cfg.Egress.PlaylistPolicy,
		"db", filepath.Join(cfg.Server.DataDir, "kiln.db"),
		"admin", "/admin",
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Info("signal received", "signal", sig.String())
	case <-ctx.Done():
	}

	shctx, shcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shcancel()
	_ = srv.Shutdown(shctx)
	cancel()
	log.Info("shutting down")
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
