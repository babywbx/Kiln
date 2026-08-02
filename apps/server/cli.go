package main

import (
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/babywbx/kiln/modules/version"
)

func runCLI(args []string, variant string, start func(string) int) int {
	if len(args) > 0 && args[0] == "service" {
		return runServiceCommand(args[1:])
	}
	if code, handled := runAsServiceIfNeeded(args, start); handled {
		return code
	}
	flags := flag.NewFlagSet("kiln", flag.ContinueOnError)
	configPath := flags.String("config", "", "path to kiln.toml or kiln.jsonc")
	showVersion := flags.Bool("version", false, "print version information")
	healthcheck := flags.String("healthcheck", "", "check an HTTP health endpoint and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Printf("kiln version=%s commit=%s built_at=%s variant=%s\n",
			version.Version, version.Commit, version.BuiltAt, variant)
		return 0
	}
	if *healthcheck != "" {
		return runHealthcheck(*healthcheck)
	}
	if strings.TrimSpace(*configPath) == "" {
		fmt.Fprintln(os.Stderr, "kiln: -config is required")
		return 2
	}
	return start(*configPath)
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
