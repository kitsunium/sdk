// Internal tests for cgroup membership resolution.
package memlimit

import (
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/service/proc/internal/cgroupfs"
)

// procSelfMountinfo lists this process's mounts (cgroupfs's).
const procSelfMountinfo string = cgroupfs.SelfMountinfoFile

// errAbsent stands in for a cgroup file that is not present.
var errAbsent = errors.New("absent")

// TestAncestorLimitFiles verifies the walk runs from the process's own cgroup
// outward to the root, which is what lets a restrictive ancestor be honoured,
// and that the walk stops at what the mount actually exposes.
func TestAncestorLimitFiles(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		root string
		path string
		want []string
	}
	tests := []tc{
		{
			name: "nested path walks outward",
			root: "/",
			path: "/a/b",
			want: []string{"/sys/fs/cgroup/a/b/memory.max", "/sys/fs/cgroup/a/memory.max", "/sys/fs/cgroup/memory.max"},
		},
		{
			name: "root path yields one file",
			root: "/",
			path: "/",
			want: []string{"/sys/fs/cgroup/memory.max"},
		},
		{name: "absent hierarchy yields nothing", root: "/", path: ""},
		{
			name: "a bind-mounted subtree is not repeated in the path",
			root: "/a",
			path: "/a/b",
			want: []string{"/sys/fs/cgroup/b/memory.max", "/sys/fs/cgroup/memory.max"},
		},
		{
			name: "a subtree this process is not in reaches nothing",
			root: "/other",
			path: "/a/b",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := ancestorLimitFiles(cgroupfs.MountValue{Point: "/sys/fs/cgroup", Root: c.root}, c.path, cgroupV2LimitFile)
		//: Order matters only for readability; membership and count carry
		//: the contract, since the caller takes a minimum.
		if len(got) != len(c.want) {
			t.Fatalf("got %d files %v, want %d %v", len(got), got, len(c.want), c.want)
		}
		//: Each file is compared in place, so a shifted walk is visible.
		for i := range got {
			//: A wrong path names another cgroup's cap or none at all.
			if got[i] != c.want[i] {
				t.Errorf("file %d = %q, want %q", i, got[i], c.want[i])
			}
		}
	}
	//: Each case is its own subtest so a failure names the shape.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
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

// TestResolveCgroupPaths_MountinfoShapes drives the two mountinfo shapes ADR
// 0075 deferred, with the lines a Linux 6.12 kernel actually wrote for them.
//
// Both were fail-safe: every candidate was a path no file answers to, so the
// derivation reported MemorySourceUnconstrained and the cap that genuinely
// bounded the process was never read.
func TestResolveCgroupPaths_MountinfoShapes(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		mountinfo  string
		membership string
		want       []string
	}
	tests := []tc{
		{
			//: `mount --bind /sys/fs/cgroup/user.slice /sys/fs/cgroup`, which is
			//: the shape a runtime produces with --cgroupns=host.
			name:       "a bind-mounted subtree is not repeated in the candidates",
			mountinfo:  "1775 1733 0:29 /user.slice /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup2 rw,nsdelegate\n",
			membership: "0::/user.slice/user-1000.slice/app.scope\n",
			want: []string{
				"/sys/fs/cgroup/user-1000.slice/app.scope/memory.max",
				"/sys/fs/cgroup/user-1000.slice/memory.max",
				"/sys/fs/cgroup/memory.max",
			},
		},
		{
			//: `mount --bind /sys/fs/cgroup "/tmp/cg dir"`.
			name:       "an escaped mount point names a real directory",
			mountinfo:  `1775 1733 0:29 / /tmp/cg\040dir rw,relatime - cgroup2 cgroup2 rw` + "\n",
			membership: "0::/app.scope\n",
			want: []string{
				"/tmp/cg dir/app.scope/memory.max",
				"/tmp/cg dir/memory.max",
			},
		},
		{
			name:       "a mount exposing a subtree we are not in contributes nothing",
			mountinfo:  "1775 1733 0:29 /system.slice /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			membership: "0::/user.slice/app.scope\n",
			want:       []string{},
		},
		{
			//: two mounts at DIFFERENT points do not shadow each other. Keeping
			//: only the last would read our own cap through the bind and lose
			//: every ancestor above what it exposes.
			name:       "a later bind does not discard the whole-hierarchy mount",
			mountinfo:  "36 35 0:31 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n99 35 0:31 /user.slice /run/x rw - cgroup2 cgroup2 rw\n",
			membership: "0::/user.slice/app.scope\n",
			want: []string{
				"/sys/fs/cgroup/user.slice/app.scope/memory.max",
				"/sys/fs/cgroup/user.slice/memory.max",
				"/sys/fs/cgroup/memory.max",
				"/run/x/app.scope/memory.max",
				"/run/x/memory.max",
			},
		},
		{
			//: the same file reached twice is opened once.
			name:       "identical mounts contribute one candidate each",
			mountinfo:  "36 35 0:31 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n37 35 0:31 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			membership: "0::/app.scope\n",
			want: []string{
				"/sys/fs/cgroup/app.scope/memory.max",
				"/sys/fs/cgroup/memory.max",
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		readFile := func(target string) ([]byte, error) {
			//: Only the two /proc files decide which paths are built.
			switch target {
			//: the mount table decides where and what is exposed.
			case procSelfMountinfo:
				return []byte(c.mountinfo), nil
			//: the membership file decides which cgroup we are in.
			case procSelfCgroup:
				return []byte(c.membership), nil
			//: no limit file is needed to check path construction.
			default:
				return nil, errAbsent
			}
		}

		got := resolveCgroupPaths(readFile)
		//: A missing or extra candidate is a cap read or missed.
		if len(got) != len(c.want) {
			t.Fatalf("got %d candidates %v, want %d %v", len(got), got, len(c.want), c.want)
		}
		//: Each candidate is compared in place, so a shifted walk is visible.
		for i := range got {
			//: a candidate naming a path no cgroup answers to is the whole
			//: defect: the cap is simply never read.
			if got[i] != c.want[i] {
				t.Errorf("candidate %d = %q, want %q", i, got[i], c.want[i])
			}
		}
	}
	//: Each case is its own subtest so a failure names the shape.
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
