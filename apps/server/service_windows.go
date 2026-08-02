//go:build windows && !lite

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const defaultServiceName = "Kiln"

type kilnService struct {
	configPath string
	start      func(string) int
}

func (s *kilnService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	exited := make(chan int, 1)
	go func() { exited <- s.start(s.configPath) }()

	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.Running, Accepts: accepted}

	for {
		select {
		case code := <-exited:
			if code != 0 {
				return false, uint32(code)
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				changes <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				requestServiceStop()
				code := <-exited
				return false, uint32(code)
			}
		}
	}
}

func runAsServiceIfNeeded(args []string, start func(string) int) (int, bool) {
	inService, err := svc.IsWindowsService()
	if err != nil || !inService {
		return 0, false
	}

	flags := flag.NewFlagSet("kiln", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	configPath := flags.String("config", "", "path to kiln.toml or kiln.jsonc")
	_ = flags.String("healthcheck", "", "")
	_ = flags.Bool("version", false, "")
	if err := flags.Parse(args); err != nil || *configPath == "" {
		return 2, true
	}

	if absolute, err := filepath.Abs(*configPath); err == nil {
		*configPath = absolute
		_ = os.Chdir(filepath.Dir(absolute))
		redirectServiceLog(filepath.Dir(absolute))
	}

	if err := svc.Run(defaultServiceName, &kilnService{configPath: *configPath, start: start}); err != nil {
		fmt.Fprintln(os.Stderr, "kiln: service run failed:", err)
		return 1, true
	}
	return 0, true
}

func redirectServiceLog(dir string) {
	path := filepath.Join(dir, "kiln.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 16<<20 {
		_ = os.Rename(path, path+".1")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	os.Stdout = file
	os.Stderr = file
}

func runServiceCommand(args []string) int {
	if len(args) == 0 {
		printServiceUsage()
		return 2
	}

	flags := flag.NewFlagSet("kiln service "+args[0], flag.ContinueOnError)
	name := flags.String("name", defaultServiceName, "Windows service name")
	configPath := flags.String("config", "", "path to kiln.toml or kiln.jsonc (install only)")
	display := flags.String("display", "Kiln Streaming Gateway", "service display name (install only)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}

	switch args[0] {
	case "install":
		return installService(*name, *display, *configPath)
	case "uninstall", "remove":
		return uninstallService(*name)
	case "start":
		return controlService(*name, "start")
	case "stop":
		return controlService(*name, "stop")
	case "status":
		return serviceStatus(*name)
	default:
		printServiceUsage()
		return 2
	}
}

func printServiceUsage() {
	fmt.Fprintln(os.Stderr, `kiln service <command>

  install    -config <path> [-name Kiln] [-display "Kiln Streaming Gateway"]
  uninstall  [-name Kiln]
  start      [-name Kiln]
  stop       [-name Kiln]
  status     [-name Kiln]

Install and uninstall need an elevated prompt.`)
}

func installService(name, display, configPath string) int {
	if configPath == "" {
		fmt.Fprintln(os.Stderr, "kiln: -config is required")
		return 2
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln:", err)
		return 1
	}
	if _, err := os.Stat(absoluteConfig); err != nil {
		fmt.Fprintln(os.Stderr, "kiln: config not readable:", err)
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln:", err)
		return 1
	}

	manager, err := mgr.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln: cannot reach the service manager (run as administrator):", err)
		return 1
	}
	defer manager.Disconnect()

	if existing, err := manager.OpenService(name); err == nil {
		existing.Close()
		fmt.Fprintf(os.Stderr, "kiln: service %q already exists\n", name)
		return 1
	}

	service, err := manager.CreateService(name, executable, mgr.Config{
		DisplayName:  display,
		Description:  "Aggregates self-hosted HLS and DASH channels into one authenticated playlist.",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, "-config", absoluteConfig)
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln: create service failed:", err)
		return 1
	}
	defer service.Close()

	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}
	if err := service.SetRecoveryActions(recovery, uint32((24 * time.Hour).Seconds())); err != nil {
		fmt.Fprintln(os.Stderr, "kiln: service installed but restart policy failed:", err)
	}

	fmt.Printf("installed service %q\n  binary %s\n  config %s\nStart it with: kiln service start -name %s\n",
		name, executable, absoluteConfig, name)
	return 0
}

func uninstallService(name string) int {
	manager, err := mgr.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln: cannot reach the service manager (run as administrator):", err)
		return 1
	}
	defer manager.Disconnect()

	service, err := manager.OpenService(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiln: service %q is not installed\n", name)
		return 1
	}
	defer service.Close()

	if status, err := service.Query(); err == nil && status.State != svc.Stopped {
		if _, err := service.Control(svc.Stop); err == nil {
			_ = waitForState(service, svc.Stopped, 20*time.Second)
		}
	}
	if err := service.Delete(); err != nil {
		fmt.Fprintln(os.Stderr, "kiln: delete failed:", err)
		return 1
	}
	fmt.Printf("removed service %q\n", name)
	return 0
}

func controlService(name, action string) int {
	manager, err := mgr.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln: cannot reach the service manager:", err)
		return 1
	}
	defer manager.Disconnect()

	service, err := manager.OpenService(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiln: service %q is not installed\n", name)
		return 1
	}
	defer service.Close()

	switch action {
	case "start":
		if err := service.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "kiln: start failed:", err)
			return 1
		}
		if err := waitForState(service, svc.Running, 30*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "kiln:", err)
			return 1
		}
		fmt.Printf("service %q is running\n", name)
	case "stop":
		if _, err := service.Control(svc.Stop); err != nil {
			fmt.Fprintln(os.Stderr, "kiln: stop failed:", err)
			return 1
		}
		if err := waitForState(service, svc.Stopped, 30*time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "kiln:", err)
			return 1
		}
		fmt.Printf("service %q is stopped\n", name)
	}
	return 0
}

func serviceStatus(name string) int {
	manager, err := mgr.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln: cannot reach the service manager:", err)
		return 1
	}
	defer manager.Disconnect()

	service, err := manager.OpenService(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kiln: service %q is not installed\n", name)
		return 1
	}
	defer service.Close()

	status, err := service.Query()
	if err != nil {
		fmt.Fprintln(os.Stderr, "kiln:", err)
		return 1
	}
	fmt.Printf("service %q is %s\n", name, stateName(status.State))
	return 0
}

func waitForState(service *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := service.Query()
		if err != nil {
			return err
		}
		if status.State == want {
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return errors.New("timed out waiting for the service to reach " + stateName(want))
}

func stateName(state svc.State) string {
	switch state {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "starting"
	case svc.StopPending:
		return "stopping"
	case svc.Running:
		return "running"
	case svc.PausePending, svc.Paused, svc.ContinuePending:
		return "paused"
	default:
		return "unknown"
	}
}
