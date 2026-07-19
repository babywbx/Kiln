package resources

import (
	"math/bits"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
)

func Detect() Limits {
	cpus, cpuMilli := detectCPUCapacity()
	limits := Limits{
		MemoryBytes: detectMemoryBytes(),
		CPUs:        cpus,
		CPUMilli:    cpuMilli,
	}
	if memoryMB, ok := positiveEnvInt64("KILN_RESOURCE_MEMORY_MB"); ok && memoryMB <= int64(^uint64(0)>>1)>>20 {
		limits.MemoryBytes = memoryMB << 20
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if cpus, ok := positiveEnvInt64("KILN_RESOURCE_CPUS"); ok &&
		cpus <= int64(^uint(0)>>1) && cpus <= maxInt64/1000 {
		limits.CPUs = int(cpus)
		limits.CPUMilli = cpus * 1000
	}
	return limits
}

func detectCPUCapacity() (int, int64) {
	detectedCPUs := runtime.GOMAXPROCS(0)
	detectedMilli := int64(detectedCPUs) * 1000
	for _, dir := range detectedCgroupDirectories("", "/sys/fs/cgroup") {
		if raw, err := os.ReadFile(path.Join(dir, "cpu.max")); err == nil {
			if quota, ok := parseCPUQuotaV2Milli(string(raw)); ok && quota < detectedMilli {
				detectedMilli = quota
			}
		}
	}
	for _, dir := range detectedCgroupDirectories("cpu", "/sys/fs/cgroup/cpu") {
		quota, quotaOK := readInt64(path.Join(dir, "cpu.cfs_quota_us"))
		period, periodOK := readInt64(path.Join(dir, "cpu.cfs_period_us"))
		if quotaOK && periodOK {
			if cpuMilli, ok := cpuQuotaMilli(quota, period); ok && cpuMilli < detectedMilli {
				detectedMilli = cpuMilli
			}
		}
	}
	return roundedUpUnits(detectedMilli, 1000), detectedMilli
}

func detectedCgroupDirectories(controller, fallback string) []string {
	self, _ := os.ReadFile("/proc/self/cgroup")
	mounts, _ := os.ReadFile("/proc/self/mountinfo")
	dirs, topologyFound := resolveCgroupDirectories(string(self), string(mounts), controller)
	if topologyFound {
		return dirs
	}
	return []string{fallback}
}

func resolveCgroupDirectories(self, mountInfo, controller string) ([]string, bool) {
	cgroupPath, ok := selfCgroupPath(self, controller)
	if !ok {
		return nil, false
	}
	var dirs []string
	seen := make(map[string]struct{})
	topologyFound := false
	for _, line := range strings.Split(mountInfo, "\n") {
		fields := strings.Fields(line)
		separator := fieldIndex(fields, "-")
		if separator < 0 || separator+3 >= len(fields) || len(fields) < 5 {
			continue
		}
		fileSystem := fields[separator+1]
		if controller == "" {
			if fileSystem != "cgroup2" {
				continue
			}
		} else if fileSystem != "cgroup" || !mountHasController(fields[separator+3:], controller) {
			continue
		}
		topologyFound = true
		root, rootOK := unescapeMountPath(fields[3])
		mountPoint, mountOK := unescapeMountPath(fields[4])
		if !rootOK || !mountOK {
			continue
		}
		leaf, ok := mountedCgroupPath(root, mountPoint, cgroupPath)
		if !ok {
			continue
		}
		for _, dir := range ancestorDirectories(leaf, mountPoint) {
			if _, exists := seen[dir]; exists {
				continue
			}
			seen[dir] = struct{}{}
			dirs = append(dirs, dir)
		}
	}
	return dirs, topologyFound
}

func selfCgroupPath(self, controller string) (string, bool) {
	for _, line := range strings.Split(self, "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		if controller == "" {
			if fields[0] == "0" && fields[1] == "" {
				return fields[2], true
			}
			continue
		}
		if listHasToken(fields[1], controller) {
			return fields[2], true
		}
	}
	return "", false
}

func mountedCgroupPath(root, mountPoint, selfPath string) (string, bool) {
	if !path.IsAbs(root) || !path.IsAbs(mountPoint) || !path.IsAbs(selfPath) {
		return "", false
	}
	if !hasPathPrefix(selfPath, root) {
		return "", false
	}
	relative := selfPath[len(root):]
	if root == "/" && selfPath != "/" {
		relative = selfPath
	}
	if hasPathPrefix(relative, "/..") || hasDotDotPathElement(relative) {
		return "", false
	}
	mountPoint = path.Clean(mountPoint)
	leaf := mountPoint + relative
	if mountPoint == "/" && relative != "" {
		leaf = relative
	}
	if leaf != mountPoint && mountPoint != "/" && !strings.HasPrefix(leaf, mountPoint+"/") {
		return "", false
	}
	return leaf, true
}

func ancestorDirectories(leaf, root string) []string {
	leaf = path.Clean(leaf)
	root = path.Clean(root)
	if leaf != root && root != "/" && !strings.HasPrefix(leaf, root+"/") {
		return nil
	}
	dirs := make([]string, 0, 4)
	for current := leaf; ; current = path.Dir(current) {
		dirs = append(dirs, current)
		if current == root {
			return dirs
		}
	}
}

func hasPathPrefix(value, prefix string) bool {
	if prefix == "/" {
		return strings.HasPrefix(value, "/")
	}
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	return len(value) == len(prefix) || value[len(prefix)] == '/'
}

func hasDotDotPathElement(value string) bool {
	for _, element := range strings.Split(value, "/") {
		if element == ".." {
			return true
		}
	}
	return false
}

func mountHasController(fields []string, controller string) bool {
	for _, field := range fields {
		if listHasToken(field, controller) {
			return true
		}
	}
	return false
}

func listHasToken(value, token string) bool {
	for _, candidate := range strings.Split(value, ",") {
		if candidate == token {
			return true
		}
	}
	return false
}

func fieldIndex(fields []string, value string) int {
	for index, field := range fields {
		if field == value {
			return index
		}
	}
	return -1
}

func unescapeMountPath(value string) (string, bool) {
	var decoded strings.Builder
	decoded.Grow(len(value))
	for index := 0; index < len(value); {
		if value[index] != '\\' {
			decoded.WriteByte(value[index])
			index++
			continue
		}
		if index+3 >= len(value) {
			return "", false
		}
		octet := 0
		for offset := 1; offset <= 3; offset++ {
			digit := value[index+offset]
			if digit < '0' || digit > '7' {
				return "", false
			}
			octet = octet*8 + int(digit-'0')
		}
		if octet > 255 {
			return "", false
		}
		decoded.WriteByte(byte(octet))
		index += 4
	}
	return decoded.String(), true
}

func parseCPUQuotaV2Milli(value string) (int64, bool) {
	fields := strings.Fields(value)
	if len(fields) != 2 || fields[0] == "max" {
		return 0, false
	}
	quota, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, false
	}
	period, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return cpuQuotaMilli(quota, period)
}

func cpuQuotaMilli(quota, period int64) (int64, bool) {
	if quota <= 0 || period <= 0 {
		return 0, false
	}
	whole := quota / period
	remainder := quota % period
	hi, lo := bits.Mul64(uint64(remainder), 1000)
	fraction, _ := bits.Div64(hi, lo, uint64(period))
	const maxInt64 = int64(^uint64(0) >> 1)
	if whole > (maxInt64-int64(fraction))/1000 {
		return 0, false
	}
	milli := whole*1000 + int64(fraction)
	if milli <= 0 {
		return 0, false
	}
	return milli, true
}

func readInt64(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return value, err == nil
}

func detectMemoryBytes() int64 {
	var detected int64
	for _, dir := range detectedCgroupDirectories("", "/sys/fs/cgroup") {
		if value, ok := readByteLimit(path.Join(dir, "memory.max")); ok && (detected == 0 || value < detected) {
			detected = value
		}
	}
	for _, dir := range detectedCgroupDirectories("memory", "/sys/fs/cgroup/memory") {
		if value, ok := readByteLimit(path.Join(dir, "memory.limit_in_bytes")); ok && (detected == 0 || value < detected) {
			detected = value
		}
	}
	if value, ok := readMemTotal("/proc/meminfo"); ok && (detected == 0 || value < detected) {
		detected = value
	}
	return detected
}

func readByteLimit(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "max" {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 || n >= 1<<60 {
		return 0, false
	}
	return n, true
}

func readMemTotal(path string) (int64, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "MemTotal:" {
			continue
		}
		kilobytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || kilobytes <= 0 {
			return 0, false
		}
		return kilobytes << 10, true
	}
	return 0, false
}

func positiveEnvInt64(name string) (int64, bool) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	return n, err == nil && n > 0
}
