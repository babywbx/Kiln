package resources_test

import (
	"testing"

	"github.com/babywbx/kiln/modules/resources"
)

func TestDetectAllowsReproducibleResourceOverrides(t *testing.T) {
	t.Setenv("KILN_RESOURCE_MEMORY_MB", "1024")
	t.Setenv("KILN_RESOURCE_CPUS", "1")

	got := resources.Detect()

	if got.MemoryBytes != 1<<30 {
		t.Fatalf("memory = %d, want %d", got.MemoryBytes, int64(1<<30))
	}
	if got.CPUs != 1 || got.CPUMilli != 1000 {
		t.Fatalf("CPU capacity = %d / %d milli, want 1 / 1000", got.CPUs, got.CPUMilli)
	}
}

func TestDetectIgnoresMemoryOverrideThatCannotBeRepresentedAsBytes(t *testing.T) {
	t.Setenv("KILN_RESOURCE_MEMORY_MB", "9223372036854775807")

	got := resources.Detect()

	if got.MemoryBytes < 0 {
		t.Fatalf("overflowing memory override produced %d bytes", got.MemoryBytes)
	}
}
