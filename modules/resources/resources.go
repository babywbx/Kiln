// Package resources derives conservative runtime budgets from effective host
// limits while leaving unconstrained systems on the existing fast path.
package resources

import "github.com/babywbx/kiln/modules/config"

type Mode string

const (
	ModeAuto        Mode = "auto"
	ModePerformance Mode = "performance"
	ModeConstrained Mode = "constrained"
)

type Limits struct {
	MemoryBytes int64
	CPUs        int
	CPUMilli    int64
}

type Inputs struct {
	Mode              Mode
	MemoryLimitMB     int
	InflightBytes     int64
	StartSegments     int
	PrefetchSegments  int
	EPGMaxConcurrency int
	EPGMaxSourceBytes int64
}

type Plan struct {
	Mode              Mode
	Constrained       bool
	MemoryLimitMB     int
	InflightBytes     int64
	StartSegments     int
	PrefetchSegments  int
	EPGMaxConcurrency int
	EPGMaxSourceBytes int64
}

func Apply(file *config.File, plan Plan) {
	file.Server.MemoryLimitMB = plan.MemoryLimitMB
	file.Packager.InflightBytes = plan.InflightBytes
	file.Packager.StartSegments = plan.StartSegments
	file.Packager.PrefetchSegments = plan.PrefetchSegments
	file.EPG.MaxRefreshConcurrency = plan.EPGMaxConcurrency
	file.EPG.MaxSourceBytes = plan.EPGMaxSourceBytes
}

const (
	oneGiB                = int64(1 << 30)
	adaptiveMemoryCeiling = 7 * oneGiB / 2
)

func Resolve(limits Limits, input Inputs) Plan {
	plan := Plan{
		Mode:              normalizedMode(input.Mode),
		MemoryLimitMB:     input.MemoryLimitMB,
		InflightBytes:     input.InflightBytes,
		StartSegments:     input.StartSegments,
		PrefetchSegments:  input.PrefetchSegments,
		EPGMaxConcurrency: input.EPGMaxConcurrency,
		EPGMaxSourceBytes: input.EPGMaxSourceBytes,
	}
	if plan.Mode == ModePerformance {
		return plan
	}

	cpuMilli := effectiveCPUMilli(limits)
	memoryConstrained := limits.MemoryBytes > 0 && limits.MemoryBytes < adaptiveMemoryCeiling
	cpuConstrained := cpuMilli > 0 && cpuMilli < 4000
	if plan.Mode == ModeConstrained {
		// Forced mode is intentionally reproducible even on a large development
		// host: use the 1 GiB / 1 CPU profile as its effective ceiling.
		if limits.MemoryBytes <= 0 || limits.MemoryBytes > oneGiB {
			limits.MemoryBytes = oneGiB
		}
		if limits.CPUs <= 0 || limits.CPUs > 1 {
			limits.CPUs = 1
		}
		limits.CPUMilli = 1000
		cpuMilli = 1000
		memoryConstrained = true
		cpuConstrained = true
	}
	plan.Constrained = plan.Mode == ModeConstrained || memoryConstrained || cpuConstrained
	if !plan.Constrained {
		return plan
	}

	if memoryConstrained {
		memoryLimitMB, inflightLimit, pipelineLimit := memoryBudgets(limits.MemoryBytes)
		plan.MemoryLimitMB = lowerPositiveInt(plan.MemoryLimitMB, memoryLimitMB)
		plan.InflightBytes = lowerPositive(plan.InflightBytes, inflightLimit)
		plan.StartSegments = lowerPositiveInt(plan.StartSegments, pipelineLimit)
		plan.PrefetchSegments = lowerPositiveInt(plan.PrefetchSegments, pipelineLimit)
		plan.EPGMaxSourceBytes = lowerPositive(plan.EPGMaxSourceBytes, epgSourceLimit(limits.MemoryBytes))
	}
	if cpuConstrained {
		pipelineLimit := roundedUpUnits(cpuMilli, 1000)
		plan.StartSegments = lowerPositiveInt(plan.StartSegments, pipelineLimit)
		plan.PrefetchSegments = lowerPositiveInt(plan.PrefetchSegments, pipelineLimit)
	}
	plan.EPGMaxConcurrency = lowerPositiveInt(plan.EPGMaxConcurrency, epgConcurrency(limits, cpuMilli))
	return plan
}

func effectiveCPUMilli(limits Limits) int64 {
	if limits.CPUMilli > 0 {
		return limits.CPUMilli
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if limits.CPUs <= 0 || int64(limits.CPUs) > maxInt64/1000 {
		return 0
	}
	return int64(limits.CPUs) * 1000
}

func epgSourceLimit(memoryBytes int64) int64 {
	limitMB := (memoryBytes >> 20) / 128
	if limitMB < 4 {
		limitMB = 4
	}
	if limitMB > 64 {
		limitMB = 64
	}
	return limitMB << 20
}

func normalizedMode(mode Mode) Mode {
	if mode == "" {
		return ModeAuto
	}
	return mode
}

func memoryBudgets(memoryBytes int64) (memoryLimitMB int, inflightBytes int64, pipeline int) {
	memoryMB := memoryBytes >> 20
	memoryLimitMB = int(memoryMB * 5 / 8)
	inflightMB := memoryMB / 32
	if inflightMB < 24 {
		inflightMB = 24
	}
	if inflightMB > 96 {
		inflightMB = 96
	}
	pipeline = memoryPipeline(memoryBytes)
	return memoryLimitMB, inflightMB << 20, pipeline
}

func memoryPipeline(memoryBytes int64) int {
	switch {
	case memoryBytes < 768<<20:
		return 1
	case memoryBytes < 5*oneGiB/2:
		return 2
	default:
		return 3
	}
}

func epgConcurrency(limits Limits, cpuMilli int64) int {
	byCPU := 0
	if cpuMilli > 0 {
		byCPU = roundedUpUnits(cpuMilli, 2000)
	}
	byMemory := 0
	if limits.MemoryBytes > 0 {
		byMemory = roundedUnits(limits.MemoryBytes, oneGiB)
	}
	switch {
	case byCPU <= 0:
		return max(byMemory, 1)
	case byMemory <= 0:
		return max(byCPU, 1)
	default:
		return max(min(byCPU, byMemory), 1)
	}
}

func roundedUpUnits(value, unit int64) int {
	units := value / unit
	if value%unit != 0 {
		units++
	}
	return positiveInt(units)
}

func roundedUnits(value, unit int64) int {
	units := value / unit
	if value%unit >= unit/2 {
		units++
	}
	return positiveInt(units)
}

func positiveInt(value int64) int {
	maxInt := int64(^uint(0) >> 1)
	if value > maxInt {
		return int(maxInt)
	}
	return max(int(value), 1)
}

func lowerPositive(current, limit int64) int64 {
	if current <= 0 || current > limit {
		return limit
	}
	return current
}

func lowerPositiveInt(current, limit int) int {
	if current <= 0 || current > limit {
		return limit
	}
	return current
}
