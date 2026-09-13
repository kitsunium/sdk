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
// outward to the root, which is what lets a restrictive ancestor be honoured,
// and that the walk stops at what the mount actually exposes.
func TestAncestorLimitFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		root string
		path string
		want []string
	}{
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
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ancestorLimitFiles(cgroupMount{point: cgroupMountRoot, root: tt.root}, tt.path, cgroupV2LimitFile)
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
		wantRoot   string
		wantFstype string
		wantOpts   string
		wantOK     bool
	}{
		{
			name:       "cgroup2 with optional fields",
			line:       "36 35 0:31 / /sys/fs/cgroup ro,nosuid shared:9 - cgroup2 cgroup2 rw",
			wantMount:  "/sys/fs/cgroup",
			wantRoot:   "/",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
			wantOK:     true,
		},
		{
			name:       "cgroup2 without optional fields",
			line:       "36 35 0:31 / /custom/cg ro - cgroup2 cgroup2 rw",
			wantMount:  "/custom/cg",
			wantRoot:   "/",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
			wantOK:     true,
		},
		{
			name:       "v1 memory controller",
			line:       "40 35 0:35 / /sys/fs/cgroup/memory rw,nosuid - cgroup cgroup rw,memory",
			wantMount:  "/sys/fs/cgroup/memory",
			wantRoot:   "/",
			wantFstype: "cgroup",
			wantOpts:   "rw,memory",
			wantOK:     true,
		},
		{
			//: verbatim from a 6.12 kernel after
			//: `mount --bind /sys/fs/cgroup/user.slice /sys/fs/cgroup`.
			name:       "a bind-mounted subtree reports its root",
			line:       "1775 1733 0:29 /user.slice /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup2 rw,nsdelegate",
			wantMount:  "/sys/fs/cgroup",
			wantRoot:   "/user.slice",
			wantFstype: "cgroup2",
			wantOpts:   "rw,nsdelegate",
			wantOK:     true,
		},
		{
			//: verbatim from a 6.12 kernel inside a cgroup namespace whose
			//: root is below the mount's root.
			name:       "a root above the namespace root keeps its dot-dots",
			line:       "1713 1710 0:29 /../../.. /sys/fs/cgroup rw,relatime - cgroup2 cgroup2 rw",
			wantMount:  "/sys/fs/cgroup",
			wantRoot:   "/../../..",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
			wantOK:     true,
		},
		{
			//: verbatim from a 6.12 kernel: the mount point is "/tmp/cg dir".
			name:       "a space in the mount point is decoded",
			line:       `36 35 0:31 / /tmp/cg\040dir ro - cgroup2 cgroup2 rw`,
			wantMount:  "/tmp/cg dir",
			wantRoot:   "/",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
			wantOK:     true,
		},
		{
			name:       "an escaped root is decoded too",
			line:       `36 35 0:31 /a\040b /sys/fs/cgroup ro - cgroup2 cgroup2 rw`,
			wantMount:  "/sys/fs/cgroup",
			wantRoot:   "/a b",
			wantFstype: "cgroup2",
			wantOpts:   "rw",
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
			if mount.point != tt.wantMount || mount.root != tt.wantRoot {
				t.Errorf("mount = (point %q, root %q), want (%q, %q)", mount.point, mount.root, tt.wantMount, tt.wantRoot)
			}
			if fstype != tt.wantFstype || opts != tt.wantOpts {
				t.Errorf("got (%q, %q), want (%q, %q)", fstype, opts, tt.wantFstype, tt.wantOpts)
			}
		})
	}
}

// TestResolveMounts verifies the hierarchies are located from mountinfo rather
// than assumed, that the conventional paths remain the fallback, and that each
// mount carries what it exposes alongside where it is attached.
func TestResolveMounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mountinfo string
		hasFile   bool
		wantV2    cgroupMount
		wantV1    cgroupMount
	}{
		{
			name:      "non-conventional mount points are honoured",
			hasFile:   true,
			mountinfo: "36 35 0:31 / /run/cg2 rw - cgroup2 cgroup2 rw\n40 35 0:35 / /run/cg1/mem rw - cgroup cgroup rw,memory\n",
			wantV2:    cgroupMount{point: "/run/cg2", root: "/"},
			wantV1:    cgroupMount{point: "/run/cg1/mem", root: "/"},
		},
		{
			name:      "v1 mount without the memory controller is ignored",
			hasFile:   true,
			mountinfo: "40 35 0:35 / /run/cpuonly rw - cgroup cgroup rw,cpu,cpuacct\n",
			wantV2:    cgroupMount{point: cgroupMountRoot, root: rootCgroupPath},
			wantV1:    cgroupMount{point: cgroupV1MemoryRoot, root: rootCgroupPath},
		},
		{
			name:    "unreadable mountinfo falls back",
			hasFile: false,
			wantV2:  cgroupMount{point: cgroupMountRoot, root: rootCgroupPath},
			wantV1:  cgroupMount{point: cgroupV1MemoryRoot, root: rootCgroupPath},
		},
		{
			//: verbatim from a 6.12 kernel: the bind shadows the whole-hierarchy
			//: mount at the same point, so the LAST line is what a path
			//: resolves through.
			name:      "a bind stacked on the same point shadows the one under it",
			hasFile:   true,
			mountinfo: "1713 1710 0:29 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n1775 1713 0:29 /user.slice /sys/fs/cgroup rw - cgroup2 cgroup2 rw\n",
			wantV2:    cgroupMount{point: cgroupMountRoot, root: "/user.slice"},
			wantV1:    cgroupMount{point: cgroupV1MemoryRoot, root: rootCgroupPath},
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

			gotV2, gotV1 := resolveMounts(readFile)
			//: Each hierarchy resolves independently of the other.
			if gotV2 != tt.wantV2 {
				t.Errorf("v2 mount = %+v, want %+v", gotV2, tt.wantV2)
			}
			if gotV1 != tt.wantV1 {
				t.Errorf("v1 mount = %+v, want %+v", gotV1, tt.wantV1)
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

// TestUnmangleMountinfoPath pins the decode of the four bytes the kernel's
// mangle_path escapes, plus the shapes that must survive it untouched.
//
// The membership path in /proc/self/cgroup is NOT mangled — a cgroup named
// "probe test" reads back with a literal 0x20 on a 6.12 kernel — so a decode
// applied to both files would corrupt a name that legitimately contains a
// backslash. It runs here and nowhere else.
func TestUnmangleMountinfoPath(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		field string
		want  string
	}
	tests := []tc{
		{name: "an ordinary path is returned untouched", field: "/sys/fs/cgroup", want: "/sys/fs/cgroup"},
		{name: "a space", field: `/tmp/cg\040dir`, want: "/tmp/cg dir"},
		{name: "a tab", field: `/tmp/a\011b`, want: "/tmp/a\tb"},
		{name: "a newline", field: `/tmp/a\012b`, want: "/tmp/a\nb"},
		{name: "a backslash", field: `/tmp/a\134b`, want: `/tmp/a\b`},
		{name: "several escapes in one field", field: `/a\040b\040c`, want: "/a b c"},
		{name: "an escape at the very end", field: `/a\040`, want: "/a "},
		{name: "a trailing marker with nothing behind it", field: `/a\`, want: `/a\`},
		{name: "a truncated escape stays verbatim", field: `/a\04`, want: `/a\04`},
		{name: "a non-octal triple stays verbatim", field: `/a\09z`, want: `/a\09z`},
		{name: "an escape decoding to a high byte", field: `/a\377b`, want: "/a\xffb"},
		{name: "an empty field", field: "", want: ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: The decode is the whole contract; a wrong byte names a wrong file.
		if got := unmangleMountinfoPath(c.field); got != c.want {
			t.Errorf("unmangleMountinfoPath(%q) = %q, want %q", c.field, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestUnderMountRoot pins the translation between the two files that disagree
// on purpose: /proc/self/cgroup names a cgroup relative to the reader's cgroup
// namespace, mountinfo field 3 names what the mount exposes relative to the
// same namespace.
func TestUnderMountRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		mountRoot  string
		cgroupPath string
		want       string
		wantOK     bool
	}
	tests := []tc{
		{
			name:      "a whole-hierarchy mount translates nothing",
			mountRoot: "/", cgroupPath: "/a/b", want: "/a/b", wantOK: true,
		},
		{
			name:      "a bind of our own subtree lands on the mount point",
			mountRoot: "/docker/abc", cgroupPath: "/docker/abc", want: "/", wantOK: true,
		},
		{
			name:      "a bind of an ancestor subtree keeps the remainder",
			mountRoot: "/user.slice", cgroupPath: "/user.slice/app.scope", want: "/app.scope", wantOK: true,
		},
		{
			name:      "a sibling subtree reaches nothing",
			mountRoot: "/system.slice", cgroupPath: "/user.slice/app.scope", wantOK: false,
		},
		{
			//: the trap a naive strings.HasPrefix falls into: "/user.slice2"
			//: starts with "/user.slice" and is a different cgroup.
			name:      "a prefix that is not a path component reaches nothing",
			mountRoot: "/user.slice", cgroupPath: "/user.slice2/app.scope", wantOK: false,
		},
		{
			//: verbatim from a 6.12 kernel inside a cgroup namespace: the mount
			//: root sits above the namespace root and is rendered with "..".
			name:      "a root above the namespace root is the identity",
			mountRoot: "/../../../../..", cgroupPath: "/", want: "/", wantOK: true,
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := underMountRoot(c.mountRoot, c.cgroupPath)
		//: Reachability decides whether the hierarchy contributes at all.
		if ok != c.wantOK {
			t.Fatalf("underMountRoot(%q, %q) ok = %t, want %t", c.mountRoot, c.cgroupPath, ok, c.wantOK)
		}
		if got != c.want {
			t.Errorf("underMountRoot(%q, %q) = %q, want %q", c.mountRoot, c.cgroupPath, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
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
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		readFile := func(target string) ([]byte, error) {
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
		if len(got) != len(c.want) {
			t.Fatalf("got %d candidates %v, want %d %v", len(got), got, len(c.want), c.want)
		}
		for i := range got {
			//: a candidate naming a path no cgroup answers to is the whole
			//: defect: the cap is simply never read.
			if got[i] != c.want[i] {
				t.Errorf("candidate %d = %q, want %q", i, got[i], c.want[i])
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
