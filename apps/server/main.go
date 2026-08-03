//go:build !lite

package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/babywbx/kiln/modules/auth"
	"github.com/babywbx/kiln/modules/catalog"
	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/debugserver"
	"github.com/babywbx/kiln/modules/distribution"
	"github.com/babywbx/kiln/modules/epg"
	"github.com/babywbx/kiln/modules/httpserver"
	"github.com/babywbx/kiln/modules/logging"
	"github.com/babywbx/kiln/modules/observe"
	"github.com/babywbx/kiln/modules/proxyegress"
	"github.com/babywbx/kiln/modules/pull"
	"github.com/babywbx/kiln/modules/resources"
	"github.com/babywbx/kiln/modules/session"
	"github.com/babywbx/kiln/modules/store"
	"github.com/babywbx/kiln/modules/telemetry"
	"github.com/babywbx/kiln/modules/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	return runCLI(args, "full", runServer)
}

func runServer(cfgPath string) int {
	boot := logging.Bootstrap()
	slog.SetDefault(boot)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		boot.Error("config load failed", "err", err, "config", cfgPath)
		return 1
	}
	runtimeVariant, validRuntimeVariant := distribution.DetectRuntimeVariant()
	resourceLimits := resources.Detect()
	resourcePlan := resources.Resolve(resourceLimits, resources.Inputs{
		Mode:              resources.Mode(cfg.Server.ResourceMode),
		MemoryLimitMB:     cfg.Server.MemoryLimitMB,
		InflightBytes:     cfg.Packager.InflightBytes,
		MaxSegmentBytes:   cfg.Packager.MaxSegmentBytes,
		StartSegments:     cfg.Packager.StartSegments,
		PrefetchSegments:  cfg.Packager.PrefetchSegments,
		EPGMaxConcurrency: cfg.EPG.MaxRefreshConcurrency,
		EPGMaxSourceBytes: cfg.EPG.MaxSourceBytes,
	})
	resources.Apply(&cfg, resourcePlan)
	configureFileCache(resourcePlan)
	log := logging.NewWith(logging.Options{
		Level:  cfg.Logging.Level,
		Format: cfg.Logging.Format,
		Color:  cfg.Logging.Color,
	})
	slog.SetDefault(log)
	if !validRuntimeVariant {
		log.Warn("unknown runtime variant; using standalone",
			"environment", os.Getenv(distribution.RuntimeVariantEnv),
		)
	}
	debugServer, err := debugserver.New(cfg.Debug.Pprof, log)
	if err != nil {
		log.Error("pprof setup failed", "err", err)
		return 1
	}
	shutdownTelemetry, err := telemetry.Setup(context.Background(), cfg.Observe, version.Version)
	tracingReady := err == nil && cfg.Observe.EnabledOrDefault() && cfg.Observe.OTLPEndpoint != ""
	if err != nil {
		log.Warn("OpenTelemetry setup failed", "err", err)
		shutdownTelemetry = func(context.Context) error { return nil }
	}

	if cfg.Server.MemoryLimitMB > 0 && os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(int64(cfg.Server.MemoryLimitMB) << 20)
	}
	gcPercent := configureGC(resourcePlan)

	if err := os.MkdirAll(cfg.Server.DataDir, 0o750); err != nil {
		log.Error("create data dir failed", "err", err, "path", cfg.Server.DataDir)
		return 1
	}

	db, err := store.Open(cfg.Server.DataDir)
	if err != nil {
		log.Error("sqlite open failed", "err", err)
		return 1
	}
	defer db.Close()
	if err := db.SeedFromConfig(cfg); err != nil {
		log.Error("sqlite seed failed", "err", err)
		return 1
	}
	users, err := db.ApplyAuthOverrides(cfg.Auth.Users)
	if err != nil {
		log.Error("auth users load failed", "err", err)
		return 1
	}
	cfg.Auth.Users = users

	egCfg, err := proxyegress.ConfigFromStore(db, cfg)
	if err != nil {
		log.Error("egress config failed", "err", err)
		return 1
	}
	egressRouter, err := proxyegress.NewRouter(egCfg)
	if err != nil {
		log.Error("egress router failed", "err", err)
		return 1
	}
	epgSvc, err := buildEPGService(cfg, db, egressRouter, log)
	if err != nil {
		log.Error("EPG init failed", "err", err)
		return 1
	}

	obs := observe.New()
	allowed := cfg.AllowedHostSet()
	authSvc, err := auth.New(cfg.Auth, cfg.TokenTTL(), auth.Options{DataDir: cfg.Server.DataDir})
	if err != nil {
		log.Error("auth init failed", "err", err)
		return 1
	}
	cat := catalog.New(cfg, db)
	puller := pull.New(pull.Options{
		Observe:     obs,
		Allowed:     allowed,
		MaxPlaylist: cfg.Security.MaxPlaylistBytes,
		Router:      egressRouter,
	})
	sessions := session.NewManager(cat, puller, obs, cfg.Server.DataDir, cfg.FFmpeg, cfg.GlobalKeys(), log, egressRouter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sessions.Start(ctx)
	if epgSvc != nil {
		epgSvc.Start(ctx)
	}

	srv := httpserver.New(httpserver.Deps{
		Cfg:      cfg,
		Auth:     authSvc,
		Catalog:  cat,
		Sessions: sessions,
		Observe:  obs,
		Store:    db,
		EPG:      epgSvc,
		Egress:   egressRouter,
		Log:      log,
		Allowed:  allowed,
		Tracing:  tracingReady,
	})

	go func() {
		if err := srv.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "err", err)
			cancel()
		}
	}()
	if debugServer != nil {
		go func() {
			if err := debugServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("pprof server stopped", "err", err)
				cancel()
			}
		}()
	}

	chs, _ := cat.List(false)
	if shouldWarnFFmpegMemory(resourcePlan, sessions.FFmpegAvailable(), cfg, chs) {
		scope := "subprocess"
		if cfg.FFmpeg.Mode == config.FFmpegModeDocker {
			scope = "external_container"
		}
		log.Warn("FFmpeg memory is outside the Go soft limit",
			"runtime_variant", runtimeVariant,
			"resource_profile", resourcePlan.Profile,
			"cgroup_memory_mb", resourceLimits.MemoryBytes>>20,
			"go_soft_limit_mb", effectiveGoMemoryLimitMB(),
			"ffmpeg_mode", cfg.FFmpeg.Mode,
			"ffmpeg_scope", scope,
			"advisory_only", true,
		)
	}
	log.Info("kiln starting",
		"version", version.Version,
		"runtime_variant", runtimeVariant,
		"config", abs(cfgPath),
		"listen", cfg.Server.Listen,
		"channels", len(chs),
		"resource_mode", resourcePlan.Mode,
		"resource_profile", resourcePlan.Profile,
		"resource_constrained", resourcePlan.Constrained,
		"effective_cpus", resourceLimits.CPUs,
		"effective_cpu_milli", resourceLimits.CPUMilli,
		"effective_memory_mb", resourceLimits.MemoryBytes>>20,
		"memory_limit_mb", cfg.Server.MemoryLimitMB,
		"effective_go_memory_limit_mb", effectiveGoMemoryLimitMB(),
		"inflight_mb", cfg.Packager.InflightBytes>>20,
		"max_segment_mb", cfg.Packager.MaxSegmentBytes>>20,
		"gc_percent", gcPercent,
		"drop_file_cache", resourcePlan.DropFileCache,
		"start_segments", cfg.Packager.StartSegments,
		"prefetch_segments", cfg.Packager.PrefetchSegments,
		"epg_refresh_concurrency", cfg.EPG.MaxRefreshConcurrency,
		"epg_max_source_mb", cfg.EPG.MaxSourceBytes>>20,
		"packager_engine", cfg.Packager.Engine,
		"ffmpeg_available", sessions.FFmpegAvailable(),
		"proxies", len(cfg.Proxies),
		"egress_default", cfg.Egress.Default,
		"playlist_policy", cfg.Egress.PlaylistPolicy,
		"db", filepath.Join(cfg.Server.DataDir, "kiln.db"),
		"epg_sources", activeEPGSourceCount(epgSvc),
		"admin", "/admin",
	)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Info("signal received", "signal", sig.String())
	case <-platformShutdown():
		log.Info("service stop requested")
	case <-ctx.Done():
	}

	shctx, shcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shcancel()
	_ = srv.Shutdown(shctx)
	if debugServer != nil {
		_ = debugServer.Shutdown(shctx)
	}
	if err := shutdownTelemetry(shctx); err != nil {
		log.Warn("OpenTelemetry shutdown failed", "err", err)
	}
	cancel()
	sessions.Shutdown()
	log.Info("shutting down")
	return 0
}

func effectiveGoMemoryLimitMB() uint64 {
	samples := []metrics.Sample{{Name: "/gc/gomemlimit:bytes"}}
	metrics.Read(samples)
	if samples[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return goMemoryLimitMB(samples[0].Value.Uint64())
}

func goMemoryLimitMB(limit uint64) uint64 {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if limit == maxInt64 {
		return 0
	}
	return limit >> 20
}

func buildEPGService(cfg config.File, db *store.DB, router *proxyegress.Router, log *slog.Logger) (*epg.Service, error) {
	rows, err := db.ListEPGSources()
	if err != nil {
		return nil, err
	}
	overrides := make([]epg.SourceOverride, 0, len(rows))
	for _, row := range rows {
		overrides = append(overrides, epg.SourceOverride{
			ID: row.ID, Name: row.Name, URL: row.URL, Timezone: row.Timezone, Proxy: row.Proxy,
			Enabled: row.Enabled, Deleted: row.Deleted,
			Revision: row.Revision, UpdatedAt: row.UpdatedAt,
		})
	}
	configured := epg.ConfigureSources(overrides)
	sources := make([]epg.Source, 0, len(configured))
	for _, source := range configured {
		if source.Enabled {
			sources = append(sources, source.Source)
		}
	}

	var cache epg.CacheStore
	if cfg.EPG.CacheEnabled() {
		cacheDirectory := strings.TrimSpace(cfg.EPG.CacheDir)
		switch strings.ToLower(cacheDirectory) {
		case "memory", ":memory:":
			cache = epg.NewMemoryStore()
		default:
			if cacheDirectory == "" {
				cacheDirectory = filepath.Join(cfg.Server.DataDir, "epg")
			}
			cache, err = epg.NewDiskStore(cacheDirectory)
			if err != nil {
				return nil, err
			}
		}
	}
	fetcher := &epg.Fetcher{
		MaxSourceBytes: cfg.EPG.MaxSourceBytes,
		UserAgent:      "Kiln EPG",
		ClientForSource: func(source epg.Source) (*http.Client, error) {
			proxy := strings.TrimSpace(source.Proxy)
			if proxy == "" || proxy == "auto" {
				return router.ClientForChannel("", "", 45*time.Second)
			}
			return router.ClientForProxy(proxy, 45*time.Second)
		},
	}
	return epg.NewService(epg.ServiceConfig{
		Sources: sources, DefaultTimezone: cfg.EPG.DefaultTimezone,
		RefreshInterval:       time.Duration(cfg.EPG.RefreshIntervalMin) * time.Minute,
		MaxRefreshConcurrency: cfg.EPG.MaxRefreshConcurrency,
		MaxSourceBytes:        cfg.EPG.MaxSourceBytes,
		OnError: func(err error) {
			log.Warn("EPG refresh failed", "err", err)
		},
	}, fetcher, cache), nil
}

func activeEPGSourceCount(service *epg.Service) int {
	if service == nil {
		return 0
	}
	return len(service.Sources())
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}
