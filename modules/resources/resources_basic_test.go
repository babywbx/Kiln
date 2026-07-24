package resources_test

import (
	"testing"

	"github.com/babywbx/kiln/modules/resources"
)

func TestResolveAutoIntegerProfiles(t *testing.T) {
	tests := []struct {
		name        string
		memoryGiB   int64
		cpus        int
		profile     resources.Profile
		constrained bool
		memoryMiB   int
		inflightMiB int64
		pipeline    int
		epg         int
	}{
		{name: "1c-1g", memoryGiB: 1, cpus: 1, profile: resources.ProfileLarge, constrained: true, inflightMiB: 96, pipeline: 1, epg: 1},
		{name: "2c-2g", memoryGiB: 2, cpus: 2, profile: resources.ProfileLarge, constrained: true, inflightMiB: 96, pipeline: 2, epg: 1},
		{name: "4c-4g", memoryGiB: 4, cpus: 4, profile: resources.ProfileLarge, inflightMiB: 96, pipeline: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := resources.Resolve(resources.Limits{
				MemoryBytes: test.memoryGiB << 30,
				CPUs:        test.cpus,
			}, resources.Inputs{
				Mode:              resources.ModeAuto,
				InflightBytes:     96 << 20,
				StartSegments:     3,
				PrefetchSegments:  3,
				EPGMaxConcurrency: 0,
			})
			if got.Profile != test.profile || got.Constrained != test.constrained || got.MemoryLimitMB != test.memoryMiB ||
				got.InflightBytes != test.inflightMiB<<20 || got.StartSegments != test.pipeline ||
				got.PrefetchSegments != test.pipeline || got.EPGMaxConcurrency != test.epg {
				t.Fatalf("plan = %+v, want profile=%s constrained=%v memory=%d MiB inflight=%d MiB pipeline=%d EPG=%d",
					got, test.profile, test.constrained, test.memoryMiB, test.inflightMiB, test.pipeline, test.epg)
			}
		})
	}
}

func TestResolveKeepsHighEndPerformanceSettings(t *testing.T) {
	input := resources.Inputs{
		Mode:              resources.ModeAuto,
		MemoryLimitMB:     256,
		InflightBytes:     96 << 20,
		StartSegments:     3,
		PrefetchSegments:  3,
		EPGMaxConcurrency: 0,
	}
	got := resources.Resolve(resources.Limits{MemoryBytes: 64 << 30, CPUs: 16}, input)

	if got.Constrained || got.MemoryLimitMB != input.MemoryLimitMB ||
		got.InflightBytes != input.InflightBytes || got.StartSegments != input.StartSegments ||
		got.PrefetchSegments != input.PrefetchSegments || got.EPGMaxConcurrency != input.EPGMaxConcurrency {
		t.Fatalf("high-end performance settings changed: got=%+v input=%+v", got, input)
	}
}
