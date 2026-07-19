//go:build extended

package resources

import (
	"reflect"
	"testing"
)

func TestParseCPUQuotaV2(t *testing.T) {
	tests := []struct {
		value string
		want  int
		ok    bool
	}{
		{value: "100000 100000", want: 1, ok: true},
		{value: "150000 100000", want: 2, ok: true},
		{value: "50000 100000", want: 1, ok: true},
		{value: "max 100000"},
		{value: "broken"},
	}
	for _, test := range tests {
		got, ok := parseCPUQuotaV2(test.value)
		if got != test.want || ok != test.ok {
			t.Errorf("parseCPUQuotaV2(%q) = %d, %v; want %d, %v", test.value, got, ok, test.want, test.ok)
		}
	}
}

func TestCPUQuotaRoundsFractionalCPUUp(t *testing.T) {
	if got, ok := cpuQuota(150000, 100000); !ok || got != 2 {
		t.Fatalf("cpuQuota = %d, %v; want 2, true", got, ok)
	}
}

func TestCPUQuotaRetainsFractionalCapacity(t *testing.T) {
	if got, ok := cpuQuotaMilli(350000, 100000); !ok || got != 3500 {
		t.Fatalf("cpuQuotaMilli = %d, %v; want 3500, true", got, ok)
	}
}

func TestCgroupDirectoriesResolveHostNamespaceAndAncestors(t *testing.T) {
	self := "0::/system.slice/kiln.service\n"
	mounts := "29 23 0:26 / /sys/fs/cgroup ro - cgroup2 cgroup rw\n"

	got := cgroupDirectories(self, mounts, "")
	want := []string{
		"/sys/fs/cgroup/system.slice/kiln.service",
		"/sys/fs/cgroup/system.slice",
		"/sys/fs/cgroup",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cgroup directories = %#v, want %#v", got, want)
	}
}

func TestCgroupDirectoriesResolveNamespacedMountRoot(t *testing.T) {
	self := "0::/\n"
	mounts := "29 23 0:26 / /sys/fs/cgroup ro - cgroup2 cgroup rw\n"

	got := cgroupDirectories(self, mounts, "")
	want := []string{"/sys/fs/cgroup"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cgroup directories = %#v, want %#v", got, want)
	}
}

func TestCgroupDirectoriesResolveBindMountedSubtree(t *testing.T) {
	self := "0::/kubepods.slice/pod/x\n"
	mounts := "29 23 0:26 /kubepods.slice /sys/fs/cgroup ro - cgroup2 cgroup rw\n"

	got := cgroupDirectories(self, mounts, "")
	want := []string{
		"/sys/fs/cgroup/pod/x",
		"/sys/fs/cgroup/pod",
		"/sys/fs/cgroup",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cgroup directories = %#v, want %#v", got, want)
	}
}

func TestCgroupDirectoriesResolveV1ControllerMount(t *testing.T) {
	self := "2:cpu,cpuacct:/docker/abc\n"
	mounts := "31 23 0:28 / /sys/fs/cgroup/cpu rw - cgroup cgroup rw,cpu,cpuacct\n"

	got := cgroupDirectories(self, mounts, "cpu")
	want := []string{
		"/sys/fs/cgroup/cpu/docker/abc",
		"/sys/fs/cgroup/cpu/docker",
		"/sys/fs/cgroup/cpu",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cgroup directories = %#v, want %#v", got, want)
	}
}

func TestCgroupDirectoriesRejectUnreachableOrMalformedPaths(t *testing.T) {
	tests := []struct {
		name   string
		self   string
		mounts string
	}{
		{
			name:   "mount root is not a component prefix",
			self:   "0::/sandbox/x\n",
			mounts: "29 23 0:26 /sand /sys/fs/cgroup ro - cgroup2 cgroup rw\n",
		},
		{
			name:   "cgroup escapes namespace",
			self:   "0::/../../init.scope\n",
			mounts: "29 23 0:26 / /sys/fs/cgroup ro - cgroup2 cgroup rw\n",
		},
		{
			name:   "wrong v2 hierarchy",
			self:   "1::/sandbox/x\n",
			mounts: "29 23 0:26 / /sys/fs/cgroup ro - cgroup2 cgroup rw\n",
		},
		{
			name:   "invalid mount escape",
			self:   "0::/sandbox/x\n",
			mounts: `29 23 0:26 / /sys/fs/cgroup\04x ro - cgroup2 cgroup rw` + "\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := cgroupDirectories(test.self, test.mounts, ""); len(got) != 0 {
				t.Fatalf("cgroup directories = %#v, want none", got)
			}
		})
	}
}

func TestAncestorDirectoriesSupportsRootMountPoint(t *testing.T) {
	got := ancestorDirectories("/system.slice/kiln.service", "/")
	want := []string{"/system.slice/kiln.service", "/system.slice", "/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ancestor directories = %#v, want %#v", got, want)
	}
}

func TestMountedCgroupPathSupportsRootMountPoint(t *testing.T) {
	got, ok := mountedCgroupPath("/", "/", "/system.slice/kiln.service")
	if !ok || got != "/system.slice/kiln.service" {
		t.Fatalf("mounted cgroup path = %q, %v; want /system.slice/kiln.service, true", got, ok)
	}
}

func TestResolveCgroupDirectoriesDoesNotRequestLegacyFallbackForKnownTopology(t *testing.T) {
	self := "0::/sandbox/x\n"
	mounts := "29 23 0:26 /other /nonstandard/cgroup ro - cgroup2 cgroup rw\n"

	dirs, topologyFound := resolveCgroupDirectories(self, mounts, "")
	if !topologyFound || len(dirs) != 0 {
		t.Fatalf("resolution = %#v, %v; want no directories with known topology", dirs, topologyFound)
	}
}

func TestUnescapeMountPathRejectsOutOfRangeOctal(t *testing.T) {
	if got, ok := unescapeMountPath(`/sys/fs/cgroup\777`); ok {
		t.Fatalf("out-of-range escape decoded as %q", got)
	}
	if got, ok := unescapeMountPath(`/sys/fs/cgroup\040slice`); !ok || got != "/sys/fs/cgroup slice" {
		t.Fatalf("valid escape decoded as %q, %v", got, ok)
	}
}

func cgroupDirectories(self, mountInfo, controller string) []string {
	dirs, _ := resolveCgroupDirectories(self, mountInfo, controller)
	return dirs
}

func parseCPUQuotaV2(value string) (int, bool) {
	milli, ok := parseCPUQuotaV2Milli(value)
	if !ok {
		return 0, false
	}
	return roundedUpUnits(milli, 1000), true
}

func cpuQuota(quota, period int64) (int, bool) {
	milli, ok := cpuQuotaMilli(quota, period)
	if !ok {
		return 0, false
	}
	return roundedUpUnits(milli, 1000), true
}
