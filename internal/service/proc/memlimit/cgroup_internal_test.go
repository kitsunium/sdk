// Internal tests for cgroup membership resolution.
package memlimit

import (
	"errors"
	"testing"
)

// errAbsent stands in for a cgroup file that is not present.
var errAbsent = errors.New("absent")

// TestParseCgroupMembership covers the shapes /proc/self/cgroup takes across
// unified, legacy and hybrid hosts — including co-mounted controller lists,
// where "memory" must match as a whole name and not as a substring.
func TestParseCgroupMembership(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantV2  string
		wantV1  string
	}{
		{name: "unified only", content: "0::/docker/abc\n", wantV2: "/docker/abc"},
		{name: "legacy memory only", content: "9:memory:/kubepods/pod1\n", wantV1: "/kubepods/pod1"},
		{name: "hybrid", content: "0::/a\n9:memory:/b\n", wantV2: "/a", wantV1: "/b"},
		{name: "co-mounted controllers", content: "4:cpu,memory,cpuacct:/x\n", wantV1: "/x"},
		{name: "memory substring must not match", content: "5:memory+swap:/y\n"},
		{name: "unrelated controller", content: "7:pids:/z\n"},
		{name: "blank lines tolerated", content: "\n0::/q\n\n", wantV2: "/q"},
		{name: "malformed line ignored", content: "garbage\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotV2, gotV1 := parseCgroupMembership(tt.content)
			//: Each hierarchy is resolved independently of the other.
			if gotV2 != tt.wantV2 {
				t.Errorf("v2 path = %q, want %q", gotV2, tt.wantV2)
			}
			if gotV1 != tt.wantV1 {
				t.Errorf("v1 path = %q, want %q", gotV1, tt.wantV1)
			}
		})
	}
}

// TestAncestorLimitFiles verifies the walk runs from the process's own cgroup
// outward to the root, which is what lets a restrictive ancestor be honoured.
func TestAncestorLimitFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "nested path walks outward",
			path: "/a/b",
			want: []string{"/sys/fs/cgroup/a/b/memory.max", "/sys/fs/cgroup/a/memory.max", "/sys/fs/cgroup/memory.max"},
		},
		{
			name: "root path yields one file",
			path: "/",
			want: []string{"/sys/fs/cgroup/memory.max"},
		},
		{name: "absent hierarchy yields nothing", path: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ancestorLimitFiles(cgroupMountRoot, tt.path, cgroupV2LimitFile)
			//: Order matters only for readability; membership and count carry
			//: the contract, since the caller takes a minimum.
			if len(got) != len(tt.want) {
				t.Fatalf("got %d files %v, want %d %v", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("file %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// TestResolveCgroupPaths_FallsBackWithoutProc verifies a host without
// /proc/self/cgroup still gets the conventional mount roots, so a namespaced
// container keeps working even when membership cannot be read.
func TestResolveCgroupPaths_FallsBackWithoutProc(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "no proc yields both roots"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := resolveCgroupPaths(func(string) ([]byte, error) { return nil, errAbsent })
			//: Both conventional roots must remain reachable.
			if len(got) != 2 {
				t.Fatalf("got %d candidates %v, want 2", len(got), got)
			}
		})
	}
}

// TestParseMountinfoLine covers the shapes /proc/self/mountinfo produces. The
// prefix carries a variable number of optional fields, so the " - " separator
// is the only reliable anchor — a positional parse would drift.
func TestParseMountinfoLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		line       string
		wantMount  string
		wantFstype string
		wantOpts   string
		wantOK     bool
	}{
		{
			name:       "cgroup2 with optional fields",
			line:       "36 35 0:31 / /sys/fs/cgroup ro,nosuid shared:9 - cgroup2 cgroup2 rw",
			wantMount:  "/sys/fs/cgroup",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
			wantOK:     true,
		},
		{
			name:       "cgroup2 without optional fields",
			line:       "36 35 0:31 / /custom/cg ro - cgroup2 cgroup2 rw",
			wantMount:  "/custom/cg",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
			wantOK:     true,
		},
		{
			name:       "v1 memory controller",
			line:       "40 35 0:35 / /sys/fs/cgroup/memory rw,nosuid - cgroup cgroup rw,memory",
			wantMount:  "/sys/fs/cgroup/memory",
			wantFstype: "cgroup",
			wantOpts:   "rw,memory",
			wantOK:     true,
		},
		{name: "line without separator", line: "36 35 0:31 / /sys/fs/cgroup rw"},
		{name: "suffix too short", line: "36 35 0:31 / /sys/fs/cgroup rw - cgroup2"},
		{name: "empty line", line: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mount, fstype, opts, ok := parseMountinfoLine(tt.line)
			//: Malformed lines must be skipped, never half-parsed.
			if ok != tt.wantOK {
				t.Fatalf("ok = %t, want %t", ok, tt.wantOK)
			}
			if mount != tt.wantMount || fstype != tt.wantFstype || opts != tt.wantOpts {
				t.Errorf("got (%q, %q, %q), want (%q, %q, %q)", mount, fstype, opts, tt.wantMount, tt.wantFstype, tt.wantOpts)
			}
		})
	}
}

// TestResolveMountPoints verifies the hierarchies are located from mountinfo
// rather than assumed, and that the conventional paths remain the fallback.
func TestResolveMountPoints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mountinfo string
		hasFile   bool
		wantV2    string
		wantV1    string
	}{
		{
			name:      "non-conventional mount points are honoured",
			hasFile:   true,
			mountinfo: "36 35 0:31 / /run/cg2 rw - cgroup2 cgroup2 rw\n40 35 0:35 / /run/cg1/mem rw - cgroup cgroup rw,memory\n",
			wantV2:    "/run/cg2",
			wantV1:    "/run/cg1/mem",
		},
		{
			name:      "v1 mount without the memory controller is ignored",
			hasFile:   true,
			mountinfo: "40 35 0:35 / /run/cpuonly rw - cgroup cgroup rw,cpu,cpuacct\n",
			wantV2:    cgroupMountRoot,
			wantV1:    cgroupV1MemoryRoot,
		},
		{
			name:    "unreadable mountinfo falls back",
			hasFile: false,
			wantV2:  cgroupMountRoot,
			wantV1:  cgroupV1MemoryRoot,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			readFile := func(target string) ([]byte, error) {
				//: Only mountinfo matters to this resolver.
				if target == procSelfMountinfo && tt.hasFile {
					return []byte(tt.mountinfo), nil
				}

				//: Anything else is absent for this test.
				return nil, errAbsent
			}

			gotV2, gotV1 := resolveMountPoints(readFile)
			//: Each hierarchy resolves independently of the other.
			if gotV2 != tt.wantV2 {
				t.Errorf("v2 root = %q, want %q", gotV2, tt.wantV2)
			}
			if gotV1 != tt.wantV1 {
				t.Errorf("v1 root = %q, want %q", gotV1, tt.wantV1)
			}
		})
	}
}

// TestResolveCgroupPaths_UsesResolvedMount verifies the limit files are built
// under the mount point mountinfo reports, not the conventional location.
// Hard-coding /sys/fs/cgroup would silently read nothing on such a host.
func TestResolveCgroupPaths_UsesResolvedMount(t *testing.T) {
	t.Parallel()

	tests := []struct{ name string }{{name: "custom mount point is used"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			readFile := func(target string) ([]byte, error) {
				switch target {
				case procSelfMountinfo:
					return []byte("36 35 0:31 / /run/cg2 rw - cgroup2 cgroup2 rw\n"), nil
				case procSelfCgroup:
					return []byte("0::/svc/app\n"), nil
				}

				//: No limit files are needed to check path construction.
				return nil, errAbsent
			}

			got := resolveCgroupPaths(readFile)
			//: The process cgroup under the resolved mount comes first.
			if len(got) == 0 || got[0] != "/run/cg2/svc/app/memory.max" {
				t.Errorf("first candidate = %v, want /run/cg2/svc/app/memory.max", got)
			}
		})
	}
}
