package main

import (
	"runtime/debug"
	"testing"

	"github.com/babywbx/kiln/modules/resources"
)

func TestConfigureGCUsesPlanUnlessEnvironmentOverridesIt(t *testing.T) {
	previous := debug.SetGCPercent(100)
	t.Cleanup(func() { debug.SetGCPercent(previous) })

	t.Setenv("GOGC", "")
	if got := configureGC(resources.Plan{GCPercent: 75}); got != "75" {
		t.Fatalf("configured GC = %q, want 75", got)
	}

	t.Setenv("GOGC", "125")
	if got := configureGC(resources.Plan{GCPercent: 50}); got != "125" {
		t.Fatalf("environment GC = %q, want 125", got)
	}
}
