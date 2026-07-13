package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/babywbx/kiln/modules/soak"
)

type options struct {
	server               string
	token                string
	username             string
	password             string
	channels             string
	output               string
	statusPath           string
	metricsPath          string
	concurrency          int
	maxConsecutiveErrors int
	duration             time.Duration
	interval             time.Duration
	stallTimeout         time.Duration
	requestTimeout       time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var opts options
	flags := flag.NewFlagSet("kiln-soak", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.server, "server", "http://127.0.0.1:8080", "Kiln server base URL")
	flags.StringVar(&opts.token, "token", os.Getenv("KILN_TOKEN"), "bearer token (or KILN_TOKEN)")
	flags.StringVar(&opts.username, "username", os.Getenv("KILN_SOAK_USERNAME"), "login username")
	flags.StringVar(&opts.password, "password", os.Getenv("KILN_SOAK_PASSWORD"), "login password")
	flags.StringVar(&opts.channels, "channels", "", "comma-separated channel IDs; empty discovers active channels")
	flags.StringVar(&opts.output, "output", "-", "JSONL output path or - for stdout")
	flags.StringVar(&opts.statusPath, "status-path", "/v1/status", "status endpoint path; empty disables")
	flags.StringVar(&opts.metricsPath, "metrics-path", "/metrics", "Prometheus endpoint path; empty disables")
	flags.IntVar(&opts.concurrency, "concurrency", 4, "maximum channels checked concurrently")
	flags.IntVar(&opts.maxConsecutiveErrors, "max-consecutive-errors", 3, "failure threshold per channel")
	flags.DurationVar(&opts.duration, "duration", 24*time.Hour, "test duration")
	flags.DurationVar(&opts.interval, "interval", 10*time.Second, "check interval")
	flags.DurationVar(&opts.stallTimeout, "stall-timeout", 2*time.Minute, "maximum time without playlist progress")
	flags.DurationVar(&opts.requestTimeout, "request-timeout", 15*time.Second, "per-request timeout")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	output, closeOutput, err := openOutput(opts.output, stdout)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "open output: %v\n", err)
		return 2
	}
	defer closeOutput()

	runner, err := soak.New(soak.Config{
		BaseURL:              opts.server,
		Token:                opts.token,
		Username:             opts.username,
		Password:             opts.password,
		Channels:             splitChannels(opts.channels),
		Concurrency:          opts.concurrency,
		Duration:             opts.duration,
		Interval:             opts.interval,
		StallTimeout:         opts.stallTimeout,
		RequestTimeout:       opts.requestTimeout,
		MaxConsecutiveErrors: opts.maxConsecutiveErrors,
		StatusPath:           opts.statusPath,
		MetricsPath:          opts.metricsPath,
	}, soak.WithOutput(output))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "configure soak: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	report, err := runner.Run(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			_, _ = fmt.Fprintln(stderr, "soak cancelled; final report was written")
		} else {
			_, _ = fmt.Fprintf(stderr, "soak failed: %v\n", err)
		}
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "soak passed: %d channels over %s\n", len(report.Channels), time.Duration(report.DurationSeconds*float64(time.Second)).Round(time.Second))
	return 0
}

func openOutput(name string, stdout io.Writer) (io.Writer, func(), error) {
	if name == "" || name == "-" {
		return stdout, func() {}, nil
	}
	file, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, func() {}, err
	}
	return file, func() { _ = file.Close() }, nil
}

func splitChannels(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.Split(raw, ",")
}
