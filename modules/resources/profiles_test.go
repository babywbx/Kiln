package resources_test

import (
	"testing"

	"github.com/babywbx/kiln/modules/resources"
)

func TestResolveAutoUsesCompactProfileForSmallCoreContainer(t *testing.T) {
	got := resources.Resolve(resources.Limits{
		MemoryBytes: 192 << 20,
		CPUs:        2,
	}, resources.Inputs{
		Mode:              resources.ModeAuto,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	})

	if got.Profile != resources.ProfileCompact || !got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want compact/true", got.Profile, got.Constrained)
	}
	if got.MemoryLimitMB != 48 || got.InflightBytes != 32<<20 ||
		got.MaxSegmentBytes != 20<<20 || got.StartSegments != 1 ||
		got.PrefetchSegments != 1 || got.GCPercent != 75 ||
		got.EPGMaxConcurrency != 1 || got.EPGMaxSourceBytes != 4<<20 {
		t.Fatalf("compact plan = %+v", got)
	}
}

func TestResolveAutoUsesBalancedProfileForMediumCoreContainer(t *testing.T) {
	got := resources.Resolve(resources.Limits{
		MemoryBytes: 384 << 20,
		CPUs:        4,
	}, resources.Inputs{
		Mode:              resources.ModeAuto,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	})

	if got.Profile != resources.ProfileBalanced || !got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want balanced/true", got.Profile, got.Constrained)
	}
	if got.MemoryLimitMB != 96 || got.InflightBytes != 48<<20 ||
		got.MaxSegmentBytes != 32<<20 || got.StartSegments != 2 ||
		got.PrefetchSegments != 2 || got.GCPercent != 100 ||
		got.EPGMaxConcurrency != 1 || got.EPGMaxSourceBytes != 4<<20 {
		t.Fatalf("balanced plan = %+v", got)
	}
}

func TestResolveAutoUsesStandardProfileBelowOneGiB(t *testing.T) {
	got := resources.Resolve(resources.Limits{
		MemoryBytes: 768 << 20,
		CPUs:        4,
	}, resources.Inputs{
		Mode:              resources.ModeAuto,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	})

	if got.Profile != resources.ProfileStandard || !got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want standard/true", got.Profile, got.Constrained)
	}
	if got.MemoryLimitMB != 192 || got.InflightBytes != 64<<20 ||
		got.MaxSegmentBytes != 32<<20 || got.StartSegments != 2 ||
		got.PrefetchSegments != 2 || got.GCPercent != 100 ||
		got.EPGMaxConcurrency != 1 || got.EPGMaxSourceBytes != 6<<20 {
		t.Fatalf("standard plan = %+v", got)
	}
}

func TestResolveAutoUsesLargeProfileAtOneGiB(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModeAuto,
		MemoryLimitMB:     0,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 1 << 30, CPUs: 4}, input)

	if got.Profile != resources.ProfileLarge || got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want large/false", got.Profile, got.Constrained)
	}
	if got.MemoryLimitMB != input.MemoryLimitMB ||
		got.InflightBytes != input.InflightBytes ||
		got.MaxSegmentBytes != input.MaxSegmentBytes ||
		got.StartSegments != input.StartSegments ||
		got.PrefetchSegments != input.PrefetchSegments ||
		got.GCPercent != 0 ||
		got.EPGMaxConcurrency != input.EPGMaxConcurrency ||
		got.EPGMaxSourceBytes != input.EPGMaxSourceBytes {
		t.Fatalf("large plan changed configured values: got=%+v input=%+v", got, input)
	}
}

func TestResolveConstrainedAlwaysUsesCompactProfile(t *testing.T) {
	got := resources.Resolve(resources.Limits{
		MemoryBytes: 64 << 30,
		CPUs:        16,
	}, resources.Inputs{
		Mode:              resources.ModeConstrained,
		MemoryLimitMB:     256,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	})

	if got.Profile != resources.ProfileCompact || !got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want compact/true", got.Profile, got.Constrained)
	}
	if got.MemoryLimitMB != 48 || got.InflightBytes != 32<<20 ||
		got.MaxSegmentBytes != 20<<20 || got.StartSegments != 1 ||
		got.PrefetchSegments != 1 || got.GCPercent != 75 ||
		got.EPGMaxConcurrency != 1 || got.EPGMaxSourceBytes != 4<<20 {
		t.Fatalf("forced compact plan = %+v", got)
	}
}

func TestResolvePerformanceUsesConfiguredProfileWithoutClamping(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModePerformance,
		MemoryLimitMB:     512,
		InflightBytes:     128 << 20,
		MaxSegmentBytes:   48 << 20,
		StartSegments:     4,
		PrefetchSegments:  5,
		EPGMaxConcurrency: 6,
		EPGMaxSourceBytes: 80 << 20,
	}

	got := resources.Resolve(resources.Limits{MemoryBytes: 128 << 20, CPUs: 1}, input)

	if got.Profile != resources.ProfileConfigured || got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want configured/false", got.Profile, got.Constrained)
	}
	if got.MemoryLimitMB != input.MemoryLimitMB ||
		got.InflightBytes != input.InflightBytes ||
		got.MaxSegmentBytes != input.MaxSegmentBytes ||
		got.StartSegments != input.StartSegments ||
		got.PrefetchSegments != input.PrefetchSegments ||
		got.GCPercent != 0 ||
		got.EPGMaxConcurrency != input.EPGMaxConcurrency ||
		got.EPGMaxSourceBytes != input.EPGMaxSourceBytes {
		t.Fatalf("performance plan changed input: got=%+v input=%+v", got, input)
	}
}

func TestResolveAutoAppliesCPUCeilingAfterMemoryProfile(t *testing.T) {
	got := resources.Resolve(resources.Limits{
		MemoryBytes: 384 << 20,
		CPUs:        1,
	}, resources.Inputs{
		Mode:              resources.ModeAuto,
		InflightBytes:     96 << 20,
		MaxSegmentBytes:   32 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 4,
		EPGMaxSourceBytes: 64 << 20,
	})

	if got.Profile != resources.ProfileBalanced || !got.Constrained {
		t.Fatalf("profile = %q constrained=%v, want balanced/true", got.Profile, got.Constrained)
	}
	if got.StartSegments != 1 || got.PrefetchSegments != 1 ||
		got.EPGMaxConcurrency != 1 {
		t.Fatalf("one-CPU balanced plan = %+v, want 1/1 media and one EPG worker", got)
	}
}

func TestResolveCachePolicyFollowsResourceProfile(t *testing.T) {
	input := resources.Inputs{
		Mode:             resources.ModeAuto,
		InflightBytes:    96 << 20,
		MaxSegmentBytes:  32 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}
	compact := resources.Resolve(resources.Limits{MemoryBytes: 192 << 20, CPUs: 2}, input)
	large := resources.Resolve(resources.Limits{MemoryBytes: 1 << 30, CPUs: 4}, input)
	input.Mode = resources.ModePerformance
	performance := resources.Resolve(resources.Limits{MemoryBytes: 192 << 20, CPUs: 1}, input)

	if !compact.DropFileCache {
		t.Fatal("compact profile did not enable page-cache reclamation")
	}
	if large.DropFileCache {
		t.Fatal("large profile unexpectedly enabled page-cache reclamation")
	}
	if performance.DropFileCache {
		t.Fatal("performance mode unexpectedly enabled page-cache reclamation")
	}
}

func TestResolveLiteReportsItsFixedProfileAndCachePolicy(t *testing.T) {
	input := resources.Inputs{
		Mode:             resources.ModeAuto,
		InflightBytes:    96 << 20,
		MaxSegmentBytes:  32 << 20,
		StartSegments:    3,
		PrefetchSegments: 3,
	}

	got := resources.ResolveLite(resources.Limits{MemoryBytes: 64 << 30, CPUs: 16}, input)

	if got.Profile != resources.ProfileLite || !got.Constrained || !got.DropFileCache {
		t.Fatalf("lite plan = %+v, want lite/constrained/cache-drop", got)
	}
}

func TestResolveAutoProfileBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		memoryMiB int64
		profile   resources.Profile
	}{
		{name: "unknown", profile: resources.ProfileConfigured},
		{name: "below-supported-core-size", memoryMiB: 127, profile: resources.ProfileCompact},
		{name: "compact-lower-bound", memoryMiB: 128, profile: resources.ProfileCompact},
		{name: "compact-upper-bound", memoryMiB: 255, profile: resources.ProfileCompact},
		{name: "balanced-lower-bound", memoryMiB: 256, profile: resources.ProfileBalanced},
		{name: "balanced-upper-bound", memoryMiB: 511, profile: resources.ProfileBalanced},
		{name: "standard-lower-bound", memoryMiB: 512, profile: resources.ProfileStandard},
		{name: "standard-upper-bound", memoryMiB: 1023, profile: resources.ProfileStandard},
		{name: "large-lower-bound", memoryMiB: 1024, profile: resources.ProfileLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resources.Resolve(resources.Limits{
				MemoryBytes: test.memoryMiB << 20,
				CPUs:        4,
			}, resources.Inputs{
				Mode:             resources.ModeAuto,
				InflightBytes:    96 << 20,
				MaxSegmentBytes:  32 << 20,
				StartSegments:    3,
				PrefetchSegments: 3,
			})
			if got.Profile != test.profile {
				t.Fatalf("profile = %q, want %q", got.Profile, test.profile)
			}
		})
	}
}

func TestResolveProfilesKeepLowerPositiveOperatorLimits(t *testing.T) {
	got := resources.Resolve(resources.Limits{
		MemoryBytes: 192 << 20,
		CPUs:        1,
	}, resources.Inputs{
		Mode:              resources.ModeAuto,
		MemoryLimitMB:     16,
		InflightBytes:     12 << 20,
		MaxSegmentBytes:   8 << 20,
		StartSegments:     1,
		PrefetchSegments:  1,
		EPGMaxConcurrency: 1,
		EPGMaxSourceBytes: 2 << 20,
	})

	if got.MemoryLimitMB != 16 || got.InflightBytes != 12<<20 ||
		got.MaxSegmentBytes != 8<<20 || got.StartSegments != 1 ||
		got.PrefetchSegments != 1 || got.EPGMaxConcurrency != 1 ||
		got.EPGMaxSourceBytes != 2<<20 {
		t.Fatalf("lower operator limits were raised: %+v", got)
	}
}
