//go:build !windows || lite

package main

import (
	"fmt"
	"os"
)

func runAsServiceIfNeeded([]string, func(string) int) (int, bool) { return 0, false }

func runServiceCommand([]string) int {
	fmt.Fprintln(os.Stderr, "kiln: service management is only available on Windows")
	return 2
}
