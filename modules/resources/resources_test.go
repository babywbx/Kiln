//go:build extended

package resources_test

import (
	"testing"

	"github.com/babywbx/kiln/modules/resources"
)

func TestResolveExtendedCPUCapsRemainIndependentFromMemoryProfiles(t *testing.T) {
	tests := []struct {
		name      string
		memoryMiB int64
		cpuMilli  int64
		profile   resources.Profile
		pipeline  int
		epg       int
	}{
		{name: "compact-one-cpu", memoryMiB: 192, cpuMilli: 1000, profile: resources.ProfileCompact, pipeline: 1, epg: 1},
		{name: "balanced-one-cpu", memoryMiB: 384, cpuMilli: 1000, profile: resources.ProfileBalanced, pipeline: 1, epg: 1},
		{name: "balanced-fractional-cpu", memoryMiB: 384, cpuMilli: 1500, profile: resources.ProfileBalanced, pipeline: 2, epg: 1},
		{name: "standard-two-cpu", memoryMiB: 768, cpuMilli: 2000, profile: resources.ProfileStandard, pipeline: 2, epg: 1},
		{name: "large-one-cpu", memoryMiB: 1024, cpuMilli: 1000, profile: resources.ProfileLarge, pipeline: 1, epg: 1},
		{name: "large-fractional-cpu", memoryMiB: 1024, cpuMilli: 1500, profile: resources.ProfileLarge, pipeline: 2, epg: 1},
		{name: "large-four-cpu", memoryMiB: 1024, cpuMilli: 4000, profile: resources.ProfileLarge, pipeline: 3, epg: 4},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resources.Resolve(resources.Limits{
				MemoryBytes: test.memoryMiB << 20,
				CPUs:        int((test.cpuMilli + 999) / 1000),
				CPUMilli:    test.cpuMilli,
			}, resources.Inputs{
				Mode:              resources.ModeAuto,
				InflightBytes:     96 << 20,
				MaxSegmentBytes:   32 << 20,
				StartSegments:     3,
				PrefetchSegments:  3,
				EPGMaxConcurrency: 4,
				EPGMaxSourceBytes: 64 << 20,
			})
			if got.Profile != test.profile || got.StartSegments != test.pipeline ||
				got.PrefetchSegments != test.pipeline || got.EPGMaxConcurrency != test.epg {
				t.Fatalf("plan = %+v, want profile=%s pipeline=%d EPG=%d",
					got, test.profile, test.pipeline, test.epg)
			}
		})
	}
}

func TestResolveExtendedUnknownMemoryKeepsConfiguredMemoryBudget(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModeAuto,
		MemoryLimitMB:     256,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	}

	got := resources.Resolve(resources.Limits{CPUMilli: 1500}, input)

	if got.Profile != resources.ProfileConfigured || !got.Constrained {
		t.Fatalf("unknown-memory plan = %+v, want configured with CPU constraint", got)
	}
	if got.MemoryLimitMB != input.MemoryLimitMB ||
		got.InflightBytes != input.InflightBytes ||
		got.MaxSegmentBytes != input.MaxSegmentBytes ||
		got.StartSegments != 2 || got.PrefetchSegments != 2 ||
		got.EPGMaxConcurrency != 1 ||
		got.EPGMaxSourceBytes != input.EPGMaxSourceBytes {
		t.Fatalf("unknown-memory plan changed memory values: got=%+v input=%+v", got, input)
	}
}

func TestResolveLiteExtendedKeepsLowerLimitsAndPerformanceOptOut(t *testing.T) {
	lower := resources.ResolveLite(resources.Limits{MemoryBytes: 64 << 30, CPUs: 16}, resources.Inputs{
		Mode:             resources.ModeAuto,
		MemoryLimitMB:    16,
		InflightBytes:    12 << 20,
		MaxSegmentBytes:  8 << 20,
		StartSegments:    1,
		PrefetchSegments: 1,
	})
	if lower.MemoryLimitMB != 16 || lower.InflightBytes != 12<<20 ||
		lower.MaxSegmentBytes != 8<<20 || lower.Profile != resources.ProfileLite ||
		!lower.DropFileCache {
		t.Fatalf("lite raised lower limits: %+v", lower)
	}

	input := resources.Inputs{
		Mode:             resources.ModePerformance,
		MemoryLimitMB:    256,
		InflightBytes:    96 << 20,
		MaxSegmentBytes:  32 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}
	performance := resources.ResolveLite(resources.Limits{MemoryBytes: 64 << 20, CPUs: 1}, input)
	if performance.Profile != resources.ProfileConfigured || performance.Constrained ||
		performance.MemoryLimitMB != input.MemoryLimitMB ||
		performance.InflightBytes != input.InflightBytes ||
		performance.MaxSegmentBytes != input.MaxSegmentBytes ||
		performance.StartSegments != input.StartSegments ||
		performance.PrefetchSegments != input.PrefetchSegments ||
		performance.GCPercent != 0 || performance.DropFileCache {
		t.Fatalf("lite performance opt-out changed: got=%+v input=%+v", performance, input)
	}
}
