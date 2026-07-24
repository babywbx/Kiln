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

type Profile string

const (
	ProfileConfigured Profile = "configured"
	ProfileCompact    Profile = "compact"
	ProfileBalanced   Profile = "balanced"
	ProfileStandard   Profile = "standard"
	ProfileLarge      Profile = "large"
	ProfileLite       Profile = "lite"
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
	MaxSegmentBytes   int64
	StartSegments     int
	PrefetchSegments  int
	EPGMaxConcurrency int
	EPGMaxSourceBytes int64
}

type Plan struct {
	Mode              Mode
	Profile           Profile
	Constrained       bool
	MemoryLimitMB     int
	InflightBytes     int64
	MaxSegmentBytes   int64
	StartSegments     int
	PrefetchSegments  int
	GCPercent         int
	DropFileCache     bool
	EPGMaxConcurrency int
	EPGMaxSourceBytes int64
}

func Apply(file *config.File, plan Plan) {
	file.Server.MemoryLimitMB = plan.MemoryLimitMB
	file.Packager.InflightBytes = plan.InflightBytes
	if plan.MaxSegmentBytes > 0 {
		file.Packager.MaxSegmentBytes = plan.MaxSegmentBytes
	}
	file.Packager.StartSegments = plan.StartSegments
	file.Packager.PrefetchSegments = plan.PrefetchSegments
	file.EPG.MaxRefreshConcurrency = plan.EPGMaxConcurrency
	file.EPG.MaxSourceBytes = plan.EPGMaxSourceBytes
}

const (
	oneGiB = int64(1 << 30)
)

type memoryBudget struct {
	profile           Profile
	memoryLimitMB     int
	inflightBytes     int64
	maxSegmentBytes   int64
	pipelineSegments  int
	gcPercent         int
	epgMaxConcurrency int
}

var (
	compactBudget = memoryBudget{
		profile:           ProfileCompact,
		memoryLimitMB:     48,
		inflightBytes:     32 << 20,
		maxSegmentBytes:   20 << 20,
		pipelineSegments:  1,
		gcPercent:         75,
		epgMaxConcurrency: 1,
	}
	balancedBudget = memoryBudget{
		profile:           ProfileBalanced,
		memoryLimitMB:     96,
		inflightBytes:     48 << 20,
		maxSegmentBytes:   32 << 20,
		pipelineSegments:  2,
		gcPercent:         100,
		epgMaxConcurrency: 1,
	}
	standardBudget = memoryBudget{
		profile:           ProfileStandard,
		memoryLimitMB:     192,
		inflightBytes:     64 << 20,
		maxSegmentBytes:   32 << 20,
		pipelineSegments:  2,
		gcPercent:         100,
		epgMaxConcurrency: 1,
	}
)

func Resolve(limits Limits, input Inputs) Plan {
	plan := Plan{
		Mode:              normalizedMode(input.Mode),
		Profile:           ProfileConfigured,
		MemoryLimitMB:     input.MemoryLimitMB,
		InflightBytes:     input.InflightBytes,
		MaxSegmentBytes:   input.MaxSegmentBytes,
		StartSegments:     input.StartSegments,
		PrefetchSegments:  input.PrefetchSegments,
		EPGMaxConcurrency: input.EPGMaxConcurrency,
		EPGMaxSourceBytes: input.EPGMaxSourceBytes,
	}
	if plan.Mode == ModePerformance {
		return plan
	}
	if plan.Mode == ModeConstrained {
		return applyMemoryBudget(plan, compactBudget, 4<<20)
	}
	if budget, epgSourceBytes, ok := automaticMemoryBudget(limits.MemoryBytes); ok {
		plan = applyMemoryBudget(plan, budget, epgSourceBytes)
		return applyCPUCaps(plan, limits)
	}
	if limits.MemoryBytes >= oneGiB {
		plan.Profile = ProfileLarge
	}
	return applyCPUCaps(plan, limits)
}

func automaticMemoryBudget(memoryBytes int64) (memoryBudget, int64, bool) {
	switch {
	case memoryBytes <= 0:
		return memoryBudget{}, 0, false
	case memoryBytes < 256<<20:
		return compactBudget, 4 << 20, true
	case memoryBytes < 512<<20:
		return balancedBudget, epgSourceLimit(memoryBytes), true
	case memoryBytes < oneGiB:
		return standardBudget, epgSourceLimit(memoryBytes), true
	default:
		return memoryBudget{}, 0, false
	}
}

func applyMemoryBudget(plan Plan, budget memoryBudget, epgSourceBytes int64) Plan {
	plan.Profile = budget.profile
	plan.Constrained = true
	plan.MemoryLimitMB = lowerPositiveInt(plan.MemoryLimitMB, budget.memoryLimitMB)
	plan.InflightBytes = lowerPositive(plan.InflightBytes, budget.inflightBytes)
	plan.MaxSegmentBytes = lowerPositive(plan.MaxSegmentBytes, budget.maxSegmentBytes)
	plan.StartSegments = lowerPositiveInt(plan.StartSegments, budget.pipelineSegments)
	plan.PrefetchSegments = lowerPositiveInt(plan.PrefetchSegments, budget.pipelineSegments)
	plan.GCPercent = budget.gcPercent
	plan.DropFileCache = true
	plan.EPGMaxConcurrency = lowerPositiveInt(plan.EPGMaxConcurrency, budget.epgMaxConcurrency)
	plan.EPGMaxSourceBytes = lowerPositive(plan.EPGMaxSourceBytes, epgSourceBytes)
	return plan
}

func applyCPUCaps(plan Plan, limits Limits) Plan {
	cpuMilli := effectiveCPUMilli(limits)
	if cpuMilli <= 0 || cpuMilli >= 4000 {
		return plan
	}
	plan.Constrained = true
	pipelineLimit := roundedUpUnits(cpuMilli, 1000)
	plan.StartSegments = lowerPositiveInt(plan.StartSegments, pipelineLimit)
	plan.PrefetchSegments = lowerPositiveInt(plan.PrefetchSegments, pipelineLimit)
	plan.EPGMaxConcurrency = lowerPositiveInt(plan.EPGMaxConcurrency, epgConcurrency(limits, cpuMilli))
	return plan
}

// ResolveLite applies a stable low-memory ceiling even on large hosts. The
// performance mode remains an explicit escape hatch for operators who prefer
// throughput over the Lite runtime contract.
func ResolveLite(limits Limits, input Inputs) Plan {
	plan := Resolve(limits, input)
	if plan.Mode == ModePerformance {
		return plan
	}
	plan.Profile = ProfileLite
	plan.Constrained = true
	plan.MemoryLimitMB = lowerPositiveInt(plan.MemoryLimitMB, 24)
	plan.InflightBytes = lowerPositive(plan.InflightBytes, 24<<20)
	plan.MaxSegmentBytes = lowerPositive(plan.MaxSegmentBytes, 20<<20)
	plan.StartSegments = lowerPositiveInt(plan.StartSegments, 1)
	plan.PrefetchSegments = lowerPositiveInt(plan.PrefetchSegments, 1)
	plan.GCPercent = 50
	plan.DropFileCache = true
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
