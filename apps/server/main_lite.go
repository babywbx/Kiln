//go:build lite

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/babywbx/kiln/modules/config"
	"github.com/babywbx/kiln/modules/liteserver"
	"github.com/babywbx/kiln/modules/logging"
	"github.com/babywbx/kiln/modules/resources"
	"github.com/babywbx/kiln/modules/version"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("kiln", flag.ContinueOnError)
	configPath := flags.String("config", "configs/examples/kiln.toml", "path to kiln.toml or kiln.jsonc")
	showVersion := flags.Bool("version", false, "print version information")
	healthcheck := flags.String("healthcheck", "", "check an HTTP health endpoint and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Printf("kiln version=%s commit=%s built_at=%s variant=lite\n",
			version.Version, version.Commit, version.BuiltAt)
		return 0
	}
	if *healthcheck != "" {
		return runHealthcheck(*healthcheck)
	}

	if os.Getenv("KILN_DEFAULT_PACKAGER_ENGINE") == "" {
		_ = os.Setenv("KILN_DEFAULT_PACKAGER_ENGINE", config.EngineNative)
	}
	boot := logging.Bootstrap()
	slog.SetDefault(boot)
	cfg, err := config.Load(*configPath)
	if err != nil {
		boot.Error("config load failed", "err", err, "config", *configPath)
		return 1
	}
	if err := validateLiteConfig(cfg); err != nil {
		boot.Error("lite config rejected", "err", err, "config", *configPath)
		return 1
	}
	resourceLimits := resources.Detect()
	resourcePlan := resources.Resolve(resourceLimits, resources.Inputs{
		Mode:              resources.Mode(cfg.Server.ResourceMode),
		MemoryLimitMB:     cfg.Server.MemoryLimitMB,
		InflightBytes:     cfg.Packager.InflightBytes,
		StartSegments:     cfg.Packager.StartSegments,
		PrefetchSegments:  cfg.Packager.PrefetchSegments,
		EPGMaxConcurrency: cfg.EPG.MaxRefreshConcurrency,
		EPGMaxSourceBytes: cfg.EPG.MaxSourceBytes,
	})
	resources.Apply(&cfg, resourcePlan)
	if cfg.Server.MemoryLimitMB > 0 && os.Getenv("GOMEMLIMIT") == "" {
		debug.SetMemoryLimit(int64(cfg.Server.MemoryLimitMB) << 20)
	}
	if err := os.MkdirAll(cfg.Server.DataDir, 0o750); err != nil {
		boot.Error("create data dir failed", "err", err, "path", cfg.Server.DataDir)
		return 1
	}
	log := logging.NewWith(logging.Options{
		Level: cfg.Logging.Level, Format: cfg.Logging.Format, Color: cfg.Logging.Color,
	})
	slog.SetDefault(log)
	log.Info("kiln lite starting",
		"version", version.Version,
		"resource_mode", resourcePlan.Mode,
		"resource_constrained", resourcePlan.Constrained,
		"effective_cpu_milli", resourceLimits.CPUMilli,
		"effective_memory_mb", resourceLimits.MemoryBytes>>20,
		"memory_limit_mb", cfg.Server.MemoryLimitMB,
		"inflight_mb", cfg.Packager.InflightBytes>>20,
		"start_segments", cfg.Packager.StartSegments,
		"prefetch_segments", cfg.Packager.PrefetchSegments,
	)
	server, err := liteserver.New(cfg, log)
	if err != nil {
		log.Error("lite server setup failed", "err", err)
		return 1
	}
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.Start() }()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	exitCode := 0
	select {
	case received := <-signals:
		log.Info("signal received", "signal", received.String())
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server stopped", "err", err)
			exitCode = 1
		}
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		log.Warn("http shutdown failed", "err", err)
	}
	return exitCode
}

func validateLiteConfig(cfg config.File) error {
	if cfg.Packager.Engine != config.EngineNative {
		return fmt.Errorf("packager.engine must be native in lite")
	}
	for _, channel := range cfg.Channels {
		if cfg.EngineFor(channel) != config.EngineNative {
			return fmt.Errorf("channel %q requires native packager engine", channel.ID)
		}
	}
	if cfg.EPG.Enabled {
		return fmt.Errorf("epg is not available in lite")
	}
	if cfg.Observe.OTLPEndpoint != "" {
		return fmt.Errorf("OpenTelemetry export is not available in lite")
	}
	if cfg.Debug.Pprof.Enabled {
		return fmt.Errorf("pprof is not available in lite")
	}
	return nil
}

func runHealthcheck(url string) int {
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		fmt.Fprintln(os.Stderr, response.Status)
		return 1
	}
	return 0
}
