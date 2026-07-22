//go:build extended

package resources_test

import (
	"testing"

	"github.com/babywbx/kiln/modules/resources"
)

const gib = int64(1 << 30)

func TestResolveKeepsExistingPerformanceOnUnconstrainedSystems(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModeAuto,
		MemoryLimitMB:     256,
		InflightBytes:     96 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 0,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 64 * gib, CPUs: 16}, input)

	if got.Constrained {
		t.Fatal("unconstrained system selected the constrained profile")
	}
	if got.MemoryLimitMB != input.MemoryLimitMB ||
		got.InflightBytes != input.InflightBytes ||
		got.StartSegments != input.StartSegments ||
		got.PrefetchSegments != input.PrefetchSegments ||
		got.EPGMaxConcurrency != input.EPGMaxConcurrency {
		t.Fatalf("performance settings changed: got=%+v input=%+v", got, input)
	}
}

func TestResolveLiteUsesBoundedRuntimeOnLargeHosts(t *testing.T) {
	got := resources.ResolveLite(resources.Limits{MemoryBytes: 64 * gib, CPUs: 16}, resources.Inputs{
		Mode:             resources.ModeAuto,
		InflightBytes:    96 << 20,
		MaxSegmentBytes:  32 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	})

	if !got.Constrained || got.MemoryLimitMB != 24 || got.InflightBytes != 24<<20 ||
		got.MaxSegmentBytes != 20<<20 || got.StartSegments != 1 || got.PrefetchSegments != 1 ||
		got.GCPercent != 50 {
		t.Fatalf("lite plan = %+v, want 24 MiB heap, 24 MiB inflight, 20 MiB segments, 1/1 pipeline, GC 50", got)
	}
}

func TestResolveLiteKeepsLowerOperatorLimits(t *testing.T) {
	got := resources.ResolveLite(resources.Limits{MemoryBytes: 64 * gib, CPUs: 16}, resources.Inputs{
		Mode:             resources.ModeAuto,
		MemoryLimitMB:    16,
		InflightBytes:    12 << 20,
		MaxSegmentBytes:  8 << 20,
		StartSegments:    1,
		PrefetchSegments: 1,
	})

	if got.MemoryLimitMB != 16 || got.InflightBytes != 12<<20 || got.MaxSegmentBytes != 8<<20 {
		t.Fatalf("lite plan raised lower limits: %+v", got)
	}
}

func TestResolveLitePerformanceModeIsExplicitOptOut(t *testing.T) {
	input := resources.Inputs{
		Mode:             resources.ModePerformance,
		MemoryLimitMB:    256,
		InflightBytes:    96 << 20,
		MaxSegmentBytes:  32 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}

	got := resources.ResolveLite(resources.Limits{MemoryBytes: 64 * gib, CPUs: 16}, input)

	if got.Constrained || got.MemoryLimitMB != input.MemoryLimitMB ||
		got.InflightBytes != input.InflightBytes || got.MaxSegmentBytes != input.MaxSegmentBytes ||
		got.StartSegments != input.StartSegments || got.PrefetchSegments != input.PrefetchSegments ||
		got.GCPercent != 0 {
		t.Fatalf("performance opt-out changed: got=%+v input=%+v", got, input)
	}
}

func TestResolveAutoBoundsOneCoreOneGiB(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModeAuto,
		InflightBytes:     96 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 0,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: gib, CPUs: 1}, input)

	if !got.Constrained {
		t.Fatal("one-core one-GiB system did not select the constrained profile")
	}
	if got.MemoryLimitMB != 640 {
		t.Fatalf("memory limit = %d MiB, want 640", got.MemoryLimitMB)
	}
	if got.InflightBytes != 32<<20 {
		t.Fatalf("inflight bytes = %d, want %d", got.InflightBytes, 32<<20)
	}
	if got.StartSegments != 1 || got.PrefetchSegments != 1 {
		t.Fatalf("pipeline = start %d prefetch %d, want 1/1", got.StartSegments, got.PrefetchSegments)
	}
	if got.EPGMaxConcurrency != 1 {
		t.Fatalf("EPG concurrency = %d, want 1", got.EPGMaxConcurrency)
	}
}

func TestResolvePerformanceModeDoesNotClampLowResourceHosts(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModePerformance,
		InflightBytes:     96 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 512 << 20, CPUs: 1}, input)

	if got.Constrained {
		t.Fatal("performance mode selected the constrained profile")
	}
	if got.InflightBytes != input.InflightBytes ||
		got.StartSegments != input.StartSegments ||
		got.PrefetchSegments != input.PrefetchSegments ||
		got.EPGMaxConcurrency != input.EPGMaxConcurrency {
		t.Fatalf("performance mode changed settings: got=%+v input=%+v", got, input)
	}
}

func TestResolveConstrainedModeForcesLowResourceBudgetOnLargeHost(t *testing.T) {
	input := resources.Inputs{
		Mode:             resources.ModeConstrained,
		InflightBytes:    96 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 64 * gib, CPUs: 16}, input)

	if !got.Constrained {
		t.Fatal("constrained mode did not select the constrained profile")
	}
	if got.MemoryLimitMB != 640 || got.InflightBytes != 32<<20 {
		t.Fatalf("memory budget = %d MiB / %d bytes, want 640 MiB / %d bytes", got.MemoryLimitMB, got.InflightBytes, 32<<20)
	}
	if got.StartSegments != 1 || got.PrefetchSegments != 1 || got.EPGMaxConcurrency != 1 {
		t.Fatalf("concurrency budget = start %d prefetch %d EPG %d, want 1/1/1", got.StartSegments, got.PrefetchSegments, got.EPGMaxConcurrency)
	}
}

func TestResolveAutoScalesThreeGiBFourCoreHostWithoutReducingMediaPipeline(t *testing.T) {
	input := resources.Inputs{
		Mode:             resources.ModeAuto,
		InflightBytes:    96 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 3 * gib, CPUs: 4}, input)

	if !got.Constrained {
		t.Fatal("three-GiB host did not select an adaptive memory profile")
	}
	if got.MemoryLimitMB != 1920 || got.InflightBytes != 96<<20 {
		t.Fatalf("memory budget = %d MiB / %d bytes, want 1920 MiB / %d bytes", got.MemoryLimitMB, got.InflightBytes, 96<<20)
	}
	if got.StartSegments != 3 || got.PrefetchSegments != 3 || got.EPGMaxConcurrency != 2 {
		t.Fatalf("pipeline = start %d prefetch %d EPG %d, want 3/3/2", got.StartSegments, got.PrefetchSegments, got.EPGMaxConcurrency)
	}
}

func TestResolveAutoScalesTwoCoreHostIndependentlyFromLargeMemory(t *testing.T) {
	input := resources.Inputs{
		Mode:             resources.ModeAuto,
		InflightBytes:    96 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 8 * gib, CPUs: 2}, input)

	if !got.Constrained {
		t.Fatal("two-core host did not select an adaptive CPU profile")
	}
	if got.MemoryLimitMB != 0 || got.InflightBytes != 96<<20 {
		t.Fatalf("memory budget = %d MiB / %d bytes, want no soft limit / %d bytes", got.MemoryLimitMB, got.InflightBytes, 96<<20)
	}
	if got.StartSegments != 2 || got.PrefetchSegments != 2 || got.EPGMaxConcurrency != 1 {
		t.Fatalf("pipeline = start %d prefetch %d EPG %d, want 2/2/1", got.StartSegments, got.PrefetchSegments, got.EPGMaxConcurrency)
	}
}

func TestResolveAutoResourceMatrix(t *testing.T) {
	tests := []struct {
		name        string
		memoryMiB   int64
		cpus        int
		constrained bool
		memoryLimit int
		inflightMiB int64
		pipeline    int
		epg         int
		epgSource   int64
	}{
		{name: "1c-512m", memoryMiB: 512, cpus: 1, constrained: true, memoryLimit: 320, inflightMiB: 24, pipeline: 1, epg: 1, epgSource: 4},
		{name: "1c-1g", memoryMiB: 1024, cpus: 1, constrained: true, memoryLimit: 640, inflightMiB: 32, pipeline: 1, epg: 1, epgSource: 8},
		{name: "1c-2g", memoryMiB: 2048, cpus: 1, constrained: true, memoryLimit: 1280, inflightMiB: 64, pipeline: 1, epg: 1, epgSource: 16},
		{name: "1c-3g", memoryMiB: 3072, cpus: 1, constrained: true, memoryLimit: 1920, inflightMiB: 96, pipeline: 1, epg: 1, epgSource: 24},
		{name: "1c-4g", memoryMiB: 4096, cpus: 1, constrained: true, inflightMiB: 96, pipeline: 1, epg: 1, epgSource: 64},
		{name: "1c-6g", memoryMiB: 6144, cpus: 1, constrained: true, inflightMiB: 96, pipeline: 1, epg: 1, epgSource: 64},
		{name: "1c-8g", memoryMiB: 8192, cpus: 1, constrained: true, inflightMiB: 96, pipeline: 1, epg: 1, epgSource: 64},
		{name: "2c-512m", memoryMiB: 512, cpus: 2, constrained: true, memoryLimit: 320, inflightMiB: 24, pipeline: 1, epg: 1, epgSource: 4},
		{name: "2c-1g", memoryMiB: 1024, cpus: 2, constrained: true, memoryLimit: 640, inflightMiB: 32, pipeline: 2, epg: 1, epgSource: 8},
		{name: "2c-2g", memoryMiB: 2048, cpus: 2, constrained: true, memoryLimit: 1280, inflightMiB: 64, pipeline: 2, epg: 1, epgSource: 16},
		{name: "2c-3g", memoryMiB: 3072, cpus: 2, constrained: true, memoryLimit: 1920, inflightMiB: 96, pipeline: 2, epg: 1, epgSource: 24},
		{name: "2c-4g", memoryMiB: 4096, cpus: 2, constrained: true, inflightMiB: 96, pipeline: 2, epg: 1, epgSource: 64},
		{name: "2c-6g", memoryMiB: 6144, cpus: 2, constrained: true, inflightMiB: 96, pipeline: 2, epg: 1, epgSource: 64},
		{name: "2c-8g", memoryMiB: 8192, cpus: 2, constrained: true, inflightMiB: 96, pipeline: 2, epg: 1, epgSource: 64},
		{name: "4c-512m", memoryMiB: 512, cpus: 4, constrained: true, memoryLimit: 320, inflightMiB: 24, pipeline: 1, epg: 1, epgSource: 4},
		{name: "4c-1g", memoryMiB: 1024, cpus: 4, constrained: true, memoryLimit: 640, inflightMiB: 32, pipeline: 2, epg: 1, epgSource: 8},
		{name: "4c-2g", memoryMiB: 2048, cpus: 4, constrained: true, memoryLimit: 1280, inflightMiB: 64, pipeline: 2, epg: 2, epgSource: 16},
		{name: "4c-3g", memoryMiB: 3072, cpus: 4, constrained: true, memoryLimit: 1920, inflightMiB: 96, pipeline: 3, epg: 2, epgSource: 24},
		{name: "4c-4g", memoryMiB: 4096, cpus: 4, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "4c-6g", memoryMiB: 6144, cpus: 4, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "4c-8g", memoryMiB: 8192, cpus: 4, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "6c-512m", memoryMiB: 512, cpus: 6, constrained: true, memoryLimit: 320, inflightMiB: 24, pipeline: 1, epg: 1, epgSource: 4},
		{name: "6c-1g", memoryMiB: 1024, cpus: 6, constrained: true, memoryLimit: 640, inflightMiB: 32, pipeline: 2, epg: 1, epgSource: 8},
		{name: "6c-2g", memoryMiB: 2048, cpus: 6, constrained: true, memoryLimit: 1280, inflightMiB: 64, pipeline: 2, epg: 2, epgSource: 16},
		{name: "6c-3g", memoryMiB: 3072, cpus: 6, constrained: true, memoryLimit: 1920, inflightMiB: 96, pipeline: 3, epg: 3, epgSource: 24},
		{name: "6c-4g", memoryMiB: 4096, cpus: 6, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "6c-6g", memoryMiB: 6144, cpus: 6, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "6c-8g", memoryMiB: 8192, cpus: 6, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "8c-512m", memoryMiB: 512, cpus: 8, constrained: true, memoryLimit: 320, inflightMiB: 24, pipeline: 1, epg: 1, epgSource: 4},
		{name: "8c-1g", memoryMiB: 1024, cpus: 8, constrained: true, memoryLimit: 640, inflightMiB: 32, pipeline: 2, epg: 1, epgSource: 8},
		{name: "8c-2g", memoryMiB: 2048, cpus: 8, constrained: true, memoryLimit: 1280, inflightMiB: 64, pipeline: 2, epg: 2, epgSource: 16},
		{name: "8c-3g", memoryMiB: 3072, cpus: 8, constrained: true, memoryLimit: 1920, inflightMiB: 96, pipeline: 3, epg: 3, epgSource: 24},
		{name: "8c-4g", memoryMiB: 4096, cpus: 8, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "8c-6g", memoryMiB: 6144, cpus: 8, inflightMiB: 96, pipeline: 3, epgSource: 64},
		{name: "8c-8g", memoryMiB: 8192, cpus: 8, inflightMiB: 96, pipeline: 3, epgSource: 64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resources.Resolve(resources.Limits{MemoryBytes: test.memoryMiB << 20, CPUs: test.cpus}, resources.Inputs{
				Mode: resources.ModeAuto, InflightBytes: 96 << 20, StartSegments: 3, PrefetchSegments: 3,
				EPGMaxSourceBytes: 64 << 20,
			})
			if got.Constrained != test.constrained || got.MemoryLimitMB != test.memoryLimit ||
				got.InflightBytes != test.inflightMiB<<20 || got.StartSegments != test.pipeline ||
				got.PrefetchSegments != test.pipeline || got.EPGMaxConcurrency != test.epg ||
				got.EPGMaxSourceBytes != test.epgSource<<20 {
				t.Fatalf("plan = %+v, want constrained=%v memory=%d MiB inflight=%d MiB pipeline=%d EPG=%d source=%d MiB",
					got, test.constrained, test.memoryLimit, test.inflightMiB, test.pipeline, test.epg, test.epgSource)
			}
		})
	}
}

func TestResolveAutoClampsUnsafeExplicitLimitsButKeepsLowerValues(t *testing.T) {
	got := resources.Resolve(resources.Limits{MemoryBytes: gib, CPUs: 1}, resources.Inputs{
		Mode:              resources.ModeAuto,
		MemoryLimitMB:     4096,
		InflightBytes:     16 << 20,
		StartSegments:     1,
		PrefetchSegments:  1,
		EPGMaxConcurrency: 8,
	})

	if got.MemoryLimitMB != 640 || got.EPGMaxConcurrency != 1 {
		t.Fatalf("unsafe explicit limits were not clamped: %+v", got)
	}
	if got.InflightBytes != 16<<20 || got.StartSegments != 1 || got.PrefetchSegments != 1 {
		t.Fatalf("lower explicit limits were raised: %+v", got)
	}
}

func TestResolveAutoScalesEPGSourceSizeWithMemory(t *testing.T) {
	got := resources.Resolve(resources.Limits{MemoryBytes: gib, CPUs: 2}, resources.Inputs{
		Mode:              resources.ModeAuto,
		InflightBytes:     96 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxSourceBytes: 64 << 20,
	})

	if got.EPGMaxSourceBytes != 8<<20 {
		t.Fatalf("EPG source limit = %d bytes, want %d", got.EPGMaxSourceBytes, 8<<20)
	}
}

func TestResolveAutoMemoryStepsAreStableAroundGiBBoundaries(t *testing.T) {
	input := resources.Inputs{
		Mode: resources.ModeAuto, InflightBytes: 96 << 20, StartSegments: 3,
		PrefetchSegments: 3, EPGMaxSourceBytes: 64 << 20,
	}
	justAboveOne := resources.Resolve(resources.Limits{MemoryBytes: gib + 1, CPUs: 4}, input)
	if justAboveOne.StartSegments != 2 || justAboveOne.PrefetchSegments != 2 || justAboveOne.EPGMaxConcurrency != 1 {
		t.Fatalf("one GiB plus one byte changed the one-GiB concurrency step: %+v", justAboveOne)
	}

	oneAndHalf := resources.Resolve(resources.Limits{MemoryBytes: 1536 << 20, CPUs: 4}, input)
	if oneAndHalf.MemoryLimitMB != 960 || oneAndHalf.InflightBytes != 48<<20 ||
		oneAndHalf.StartSegments != 2 || oneAndHalf.PrefetchSegments != 2 || oneAndHalf.EPGMaxConcurrency != 2 {
		t.Fatalf("one-and-a-half GiB plan = %+v, want 960 MiB / 48 MiB / 2 / 2", oneAndHalf)
	}
}

func TestResolveAutoTreatsNominalFourGBHostAsUnconstrained(t *testing.T) {
	input := resources.Inputs{
		Mode: resources.ModeAuto, InflightBytes: 96 << 20, StartSegments: 3,
		PrefetchSegments: 3, EPGMaxSourceBytes: 64 << 20,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 3700 << 20, CPUs: 4}, input)

	if got.Constrained || got.MemoryLimitMB != 0 || got.InflightBytes != input.InflightBytes ||
		got.StartSegments != input.StartSegments || got.PrefetchSegments != input.PrefetchSegments ||
		got.EPGMaxConcurrency != 0 || got.EPGMaxSourceBytes != input.EPGMaxSourceBytes {
		t.Fatalf("nominal four-GB host was reduced: %+v", got)
	}
}

func TestResolveAutoUsesFractionalCPUQuotaWithoutEnteringFastPathEarly(t *testing.T) {
	input := resources.Inputs{
		Mode: resources.ModeAuto, InflightBytes: 96 << 20, StartSegments: 3,
		PrefetchSegments: 3, EPGMaxSourceBytes: 64 << 20,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 8 * gib, CPUs: 4, CPUMilli: 3500}, input)

	if !got.Constrained || got.StartSegments != 3 || got.PrefetchSegments != 3 || got.EPGMaxConcurrency != 2 {
		t.Fatalf("3.5-CPU plan = %+v, want constrained 3/3/2", got)
	}
}

func TestResolveAutoDerivesPipelineFromMilliCPUCapacity(t *testing.T) {
	input := resources.Inputs{
		Mode: resources.ModeAuto, InflightBytes: 96 << 20, StartSegments: 3,
		PrefetchSegments: 3, EPGMaxSourceBytes: 64 << 20,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 8 * gib, CPUMilli: 1500}, input)

	if !got.Constrained || got.StartSegments != 2 || got.PrefetchSegments != 2 || got.EPGMaxConcurrency != 1 {
		t.Fatalf("1.5-CPU plan = %+v, want constrained 2/2/1", got)
	}
}
