//go:build linux

// Package cgroup — white-box tests for the Linux cgroup v2 implementation.
//
// A control group is a directory, and every method here is a write into it, so a
// temporary directory stands in for a delegated group perfectly: the same code
// path runs, and the failure modes (an absent directory, a file that cannot be
// written) are reachable without root or a delegated hierarchy.
package cgroup

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fakeGroup returns a controlGroup bound to a fresh temporary directory, which
// behaves like a delegated control group for every write this package performs.
func fakeGroup(t *testing.T) *controlGroup {
	t.Helper()
	return &controlGroup{dir: t.TempDir()}
}

// missingGroup returns a controlGroup bound to a directory that does not exist,
// which is how the ENOENT branches become reachable.
func missingGroup(t *testing.T) *controlGroup {
	t.Helper()
	return &controlGroup{dir: filepath.Join(t.TempDir(), "absent")}
}

// fieldValue returns the StringValue of the first field keyed key, or "".
func fieldValue(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		//: the first match wins; fields merge along the wrap chain.
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// Test_available pins that the probe answers without panicking and agrees with
// itself. It cannot assert a fixed value — whether this host delegates cgroup v2
// is a property of the machine — but a probe that flips between calls would be
// a bug on every machine, and that IS assertable.
func Test_available(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		calls int
	}
	tests := []tc{
		{"a single probe", 1},
		{"five probes in a row", 5},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		first := available()
		for range c.calls {
			//: the probe creates and removes a uniquely-named directory; a
			//: fixed name would false-negative on its own leftovers.
			if got := available(); got != first {
				t.Fatalf("available() flipped to %v after reporting %v", got, first)
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

// Test_validateName pins the containment guarantee. Every rejected form, joined
// onto the delegated root, would resolve outside it — and the check runs before
// filepath.Join ever sees the name, so a crafted one cannot reach the
// filesystem at all.
func Test_validateName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		in      string
		wantErr bool
	}
	tests := []tc{
		{name: "a plain name", in: "sdk-group"},
		{name: "a name with digits", in: "group42"},
		{name: "a name with a dot inside", in: "a.b"},
		{name: "a hidden name", in: ".hidden"},
		{name: "an empty name", in: "", wantErr: true},
		{name: "the current directory", in: ".", wantErr: true},
		{name: "the parent directory", in: "..", wantErr: true},
		{name: "a traversal", in: "../escape", wantErr: true},
		{name: "a nested path", in: "a/b", wantErr: true},
		{name: "an absolute path", in: "/abs", wantErr: true},
		{name: "a trailing separator", in: "foo/", wantErr: true},
		{name: "a doubled separator", in: "a//b", wantErr: true},
		{name: "a non-canonical form", in: "./a", wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := validateName(c.in)
		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
				t.Fatalf("validateName(%q) = %v, want INVALID_SPEC", c.in, err)
			}
			//: the offending name must ride along, or an operator sees only
			//: "invalid spec" with no idea which group failed.
			if got := fieldValue(err, "name"); got != c.in {
				t.Errorf("the name field is %q, want %q", got, c.in)
			}
			return
		}
		if err != nil {
			t.Fatalf("validateName(%q) = %v, want nil", c.in, err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_withinRoot pins the defence in depth behind validateName. It judges the
// path AFTER the join, so a name that slipped through the first check still
// cannot land a control group outside the delegated subtree. The root itself is
// deliberately not a valid leaf: a name that collapsed to it would let a caller
// operate on the delegation root.
func Test_withinRoot(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		root string
		dir  string
		want bool
	}
	tests := []tc{
		{"a direct child", "/sys/fs/cgroup", "/sys/fs/cgroup/app", true},
		{"a deeper descendant", "/sys/fs/cgroup", "/sys/fs/cgroup/a/b", true},
		{"the root itself", "/sys/fs/cgroup", "/sys/fs/cgroup", false},
		{"the parent of the root", "/sys/fs/cgroup", "/sys/fs", false},
		{"a sibling", "/sys/fs/cgroup", "/sys/fs/cgroup2", false},
		//: a prefix match without the separator is exactly how a naive
		//: HasPrefix check lets a sibling through.
		{"a sibling sharing the prefix", "/sys/fs/cgroup", "/sys/fs/cgroupevil", false},
		{"an unrelated path", "/sys/fs/cgroup", "/tmp/x", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := withinRoot(c.root, c.dir); got != c.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", c.root, c.dir, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_invalidName pins the refusal wrapper: one code, the usage exit status,
// and the offending name attached.
func Test_invalidName(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   string
	}
	tests := []tc{
		{"a traversal", "../escape"},
		{"an empty name", ""},
		{"a nested path", "a/b"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := invalidName(c.in)
		if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
			t.Fatalf("invalidName(%q) = %v, want INVALID_SPEC", c.in, err)
		}
		//: EX_USAGE says the caller got it wrong, which is what distinguishes
		//: a bad name from a host that cannot honour a good one.
		if got := errs.ExitCodeOf(err); got != exitUsage {
			t.Errorf("exit code = %d, want %d", got, exitUsage)
		}
		if got := fieldValue(err, "name"); got != c.in {
			t.Errorf("the name field is %q, want %q", got, c.in)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_notDelegated pins which mkdir failures mean "this root is not yours".
// The distinction decides whether the caller sees CGROUP_UNAVAILABLE — a
// deployment fact they can act on — or CGROUP_CREATE_FAILED, which says
// something genuinely went wrong.
func Test_notDelegated(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		cause error
		want  bool
	}
	tests := []tc{
		{"permission denied", syscall.EACCES, true},
		{"operation not permitted", syscall.EPERM, true},
		{"a read-only filesystem", syscall.EROFS, true},
		{"no such file", syscall.ENOENT, false},
		{"already exists", syscall.EEXIST, false},
		{"out of space", syscall.ENOSPC, false},
		{"a wrapped permission denial", os.NewSyscallError("mkdir", syscall.EACCES), true},
		{"a wrapped read-only filesystem", os.NewSyscallError("mkdir", syscall.EROFS), true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := notDelegated(c.cause); got != c.want {
			t.Errorf("notDelegated(%v) = %v, want %v", c.cause, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_classifyCreate pins the routing that follows from notDelegated: a
// delegation denial is reported as unavailable so a caller can fall back, and
// anything else as a create failure so it does not silently disable
// confinement.
func Test_classifyCreate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		cause    error
		wantCode errs.Code
		wantExit int
	}
	tests := []tc{
		{"permission denied", syscall.EACCES, coreproc.CodeCgroupUnavailable, exitUnavailable},
		{"a read-only filesystem", syscall.EROFS, coreproc.CodeCgroupUnavailable, exitUnavailable},
		{"out of space", syscall.ENOSPC, coreproc.CodeCgroupCreateFailed, exitOSErr},
		{"already exists", syscall.EEXIST, coreproc.CodeCgroupCreateFailed, exitOSErr},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := classifyCreate(c.cause, "/sys/fs/cgroup/app")
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("classifyCreate(%v) = %v, want code %v", c.cause, err, c.wantCode)
		}
		if got := errs.ExitCodeOf(err); got != c.wantExit {
			t.Errorf("exit code = %d, want %d", got, c.wantExit)
		}
		//: the directory must ride along either way, or an operator cannot
		//: tell which group failed.
		if got := fieldValue(err, "dir"); got != "/sys/fs/cgroup/app" {
			t.Errorf("the dir field is %q", got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cgroupUnavailable pins the shared wrapper: the central code, the
// EX_UNAVAILABLE exit status a supervisor reads, and whichever path key the
// caller chose.
func Test_cgroupUnavailable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		key   string
		val   string
		cause error
	}
	tests := []tc{
		{"a missing root", "root", "/sys/fs/cgroup", syscall.ENOENT},
		{"an undelegated directory", "dir", "/sys/fs/cgroup/app", syscall.EACCES},
		{"a nil cause", "root", "/sys/fs/cgroup", nil},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := cgroupUnavailable(c.cause, c.key, c.val)
		if !errs.HasCode(err, coreproc.CodeCgroupUnavailable) {
			t.Fatalf("cgroupUnavailable = %v, want CGROUP_UNAVAILABLE", err)
		}
		//: EX_UNAVAILABLE says the facility is missing, not that the caller
		//: asked for something wrong.
		if got := errs.ExitCodeOf(err); got != exitUnavailable {
			t.Errorf("exit code = %d, want %d", got, exitUnavailable)
		}
		if got := fieldValue(err, c.key); got != c.val {
			t.Errorf("the %s field is %q, want %q", c.key, got, c.val)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_cgroupUnsupported pins the kernel-too-old signal. It is what tells a
// caller to fall back to a process-group kill instead of assuming the group is
// unkillable, so the code has to be the platform sentinel and not a write
// failure.
func Test_cgroupUnsupported(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		file string
	}
	tests := []tc{
		{"the kill interface", killFile},
		{"the freeze interface", freezeFile},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		err := cgroupUnsupported(os.ErrNotExist, c.file)
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("cgroupUnsupported = %v, want UNSUPPORTED_PLATFORM", err)
		}
		if got := fieldValue(err, "file"); got != c.file {
			t.Errorf("the file field is %q, want %q", got, c.file)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_maxOrValue pins the "no limit" convention. A negative value means
// unlimited across the whole SDK, and the kernel spells that as the literal
// "max" — writing "-1" instead would be rejected, and writing "0" would be a
// ceiling of zero, which is the opposite of what was asked.
func Test_maxOrValue(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   int64
		want string
	}
	tests := []tc{
		{"a positive ceiling", 1024, "1024"},
		{"zero", 0, "0"},
		{"minus one means unlimited", -1, unlimited},
		{"any negative means unlimited", -4096, unlimited},
		{"the largest ceiling", 1<<62 - 1, "4611686018427387903"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := maxOrValue(c.in); got != c.want {
			t.Errorf("maxOrValue(%d) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_writeController pins what lands in the interface file and
// what happens when the write fails. A cgroup v2 file takes ONE write with no
// trailing newline, so an extra byte would be rejected by the kernel.
func Test_controlGroup_writeController(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		file  string
		value string
	}
	tests := []tc{
		{"a memory ceiling", "memory.max", "67108864"},
		{"an unlimited ceiling", "memory.max", unlimited},
		{"a cpu quota pair", "cpu.max", "100000 100000"},
		{"a pid to attach", procsFile, "4242"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := g.writeController(c.file, c.value); err != nil {
			t.Fatalf("writeController(%s) = %v, want nil", c.file, err)
		}

		got, rerr := os.ReadFile(filepath.Join(g.dir, c.file))
		if rerr != nil {
			t.Fatalf("reading back %s: %v", c.file, rerr)
		}
		//: exactly the value, with nothing appended — a trailing newline is a
		//: byte the kernel would reject.
		if string(got) != c.value {
			t.Errorf("%s contains %q, want %q", c.file, got, c.value)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a write into a group whose directory is gone is a controller failure,
	//: annotated with the file so a log names the controller that refused.
	g := missingGroup(t)
	err := g.writeController("memory.max", "1")
	if !errs.HasCode(err, coreproc.CodeCgroupWriteFailed) {
		t.Fatalf("writeController into a missing group = %v, want CGROUP_WRITE_FAILED", err)
	}
	if got := fieldValue(err, "file"); got != "memory.max" {
		t.Errorf("the file field is %q, want memory.max", got)
	}
}

// Test_controlGroup_writeFeature pins the version gate. An absent interface file
// means the running kernel predates the feature, which is a fallback signal
// rather than a fault — and it has to be distinguishable from a write that
// failed for any other reason.
func Test_controlGroup_writeFeature(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		file  string
		value string
	}
	tests := []tc{
		{"the kill trigger", killFile, killTrigger},
		{"freezing", freezeFile, freezeOn},
		{"thawing", freezeFile, freezeOff},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := g.writeFeature(c.file, c.value); err != nil {
			t.Fatalf("writeFeature(%s) = %v, want nil", c.file, err)
		}
		got, rerr := os.ReadFile(filepath.Join(g.dir, c.file))
		if rerr != nil {
			t.Fatalf("reading back %s: %v", c.file, rerr)
		}
		if string(got) != c.value {
			t.Errorf("%s contains %q, want %q", c.file, got, c.value)
		}

		//: the same write into a group whose directory is gone reports the
		//: kernel-too-old signal, which is what a caller falls back on.
		missing := missingGroup(t)
		err := missing.writeFeature(c.file, c.value)
		if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
			t.Fatalf("writeFeature into a missing group = %v, want UNSUPPORTED_PLATFORM", err)
		}
		if fv := fieldValue(err, "file"); fv != c.file {
			t.Errorf("the file field is %q, want %q", fv, c.file)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_Delete pins that removal reports a populated group rather
// than pretending success. EBUSY here is the caller's signal that something is
// still running inside the confinement they are trying to tear down.
func Test_controlGroup_Delete(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		populate bool
		wantErr  bool
	}
	tests := []tc{
		{name: "an empty group"},
		{name: "a group with a file in it", populate: true, wantErr: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)
		if c.populate {
			//: a non-empty directory is what a populated cgroup looks like to
			//: rmdir(2), which is the call Delete makes.
			if err := os.WriteFile(filepath.Join(g.dir, "occupant"), []byte("x"), 0o600); err != nil {
				t.Fatalf("populating the group: %v", err)
			}
		}

		err := g.Delete()

		if c.wantErr {
			if !errs.HasCode(err, coreproc.CodeCgroupDeleteFailed) {
				t.Fatalf("Delete on a populated group = %v, want CGROUP_DELETE_FAILED", err)
			}
			if got := fieldValue(err, "dir"); got != g.dir {
				t.Errorf("the dir field is %q, want %q", got, g.dir)
			}
			return
		}
		if err != nil {
			t.Fatalf("Delete = %v, want nil", err)
		}
		//: the directory must actually be gone.
		if _, serr := os.Stat(g.dir); serr == nil {
			t.Error("Delete reported success but the directory is still there")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// assertOnlyWrote checks that the group directory holds exactly one file, named
// file and containing want.
//
// The "exactly one" part is the point: a setter that also touched a second
// controller would constrain something the caller never named, and a per-file
// read alone would never notice.
func assertOnlyWrote(t *testing.T, g *controlGroup, file, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(g.dir, file))
	if err != nil {
		t.Fatalf("reading back %s: %v", file, err)
	}
	//: exactly the value, with nothing appended — a trailing newline is a byte
	//: the kernel would reject.
	if string(got) != want {
		t.Errorf("%s contains %q, want %q", file, got, want)
	}
	entries, derr := os.ReadDir(g.dir)
	if derr != nil {
		t.Fatalf("listing the group: %v", derr)
	}
	for _, e := range entries {
		if e.Name() != file {
			t.Errorf("the call also wrote %s", e.Name())
		}
	}
}

// Test_controlGroup_SetMemoryMax pins the memory ceiling.
//
// A negative value means unlimited across the SDK and the kernel spells that
// as the literal "max"; writing "-1" would be rejected and "0" would be a
// ceiling of zero, which is the opposite of what was asked.
func Test_controlGroup_SetMemoryMax(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		set  func(g *controlGroup) error
		want string
	}
	tests := []tc{
		{"a byte ceiling", func(g *controlGroup) error { return g.SetMemoryMax(1 << 20) }, "1048576"},
		{"a zero ceiling", func(g *controlGroup) error { return g.SetMemoryMax(0) }, "0"},
		{"unlimited", func(g *controlGroup) error { return g.SetMemoryMax(-1) }, unlimited},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := c.set(g); err != nil {
			t.Fatalf("%s = %v, want nil", c.name, err)
		}

		assertOnlyWrote(t, g, "memory.max", c.want)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_SetCPUMax pins the CPU quota and period pair.
//
// cpu.max takes a PAIR, and only the quota half accepts "max" — a setter that
// applied the unlimited spelling to the period would write a line the kernel
// rejects, silently leaving the previous quota in force.
func Test_controlGroup_SetCPUMax(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		set  func(g *controlGroup) error
		want string
	}
	tests := []tc{
		{"a bounded quota", func(g *controlGroup) error { return g.SetCPUMax(50000, 100000) }, "50000 100000"},
		{"a zero quota", func(g *controlGroup) error { return g.SetCPUMax(0, 100000) }, "0 100000"},
		{"unlimited", func(g *controlGroup) error { return g.SetCPUMax(-1, 100000) }, unlimited + " 100000"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := c.set(g); err != nil {
			t.Fatalf("%s = %v, want nil", c.name, err)
		}

		assertOnlyWrote(t, g, "cpu.max", c.want)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_SetPidsMax pins the process ceiling.
//
// A process ceiling of zero is a real, and very different, request from no
// ceiling at all: it forbids the group from forking anything.
func Test_controlGroup_SetPidsMax(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		set  func(g *controlGroup) error
		want string
	}
	tests := []tc{
		{"a bounded count", func(g *controlGroup) error { return g.SetPidsMax(64) }, "64"},
		{"a zero count", func(g *controlGroup) error { return g.SetPidsMax(0) }, "0"},
		{"unlimited", func(g *controlGroup) error { return g.SetPidsMax(-1) }, unlimited},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := c.set(g); err != nil {
			t.Fatalf("%s = %v, want nil", c.name, err)
		}

		assertOnlyWrote(t, g, "pids.max", c.want)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_SetIOMax pins one io.max line, verbatim.
//
// io.max is free-form, so the setter must pass the line through untouched — an
// added newline or a normalised spelling would be rejected by the kernel.
func Test_controlGroup_SetIOMax(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		set  func(g *controlGroup) error
		want string
	}
	tests := []tc{
		{"a read-bandwidth line", func(g *controlGroup) error { return g.SetIOMax("8:0 rbps=1048576") }, "8:0 rbps=1048576"},
		{"a write-bandwidth line", func(g *controlGroup) error { return g.SetIOMax("8:0 wbps=max") }, "8:0 wbps=max"},
		{"an empty line", func(g *controlGroup) error { return g.SetIOMax("") }, ""},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := c.set(g); err != nil {
			t.Fatalf("%s = %v, want nil", c.name, err)
		}

		assertOnlyWrote(t, g, "io.max", c.want)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_Add pins the pid to attach.
//
// cgroup.procs takes ONE pid per write, in decimal. Confinement only applies
// to a process once this write lands, so a malformed value here is the
// difference between a constrained child and an unconstrained one.
func Test_controlGroup_Add(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		set  func(g *controlGroup) error
		want string
	}
	tests := []tc{
		{"a plausible pid", func(g *controlGroup) error { return g.Add(4242) }, "4242"},
		{"pid 1", func(g *controlGroup) error { return g.Add(1) }, "1"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g := fakeGroup(t)

		if err := c.set(g); err != nil {
			t.Fatalf("%s = %v, want nil", c.name, err)
		}

		assertOnlyWrote(t, g, procsFile, c.want)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_Kill pins the atomic subtree kill.
//
// Membership is tracked by control group, not process group, so a member that
// called setsid is still killed — which is exactly what a kill(-pgid) misses.
func Test_controlGroup_Kill(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: whether the group directory exists; an absent one is how the
		//: kernel-too-old branch becomes reachable without an old kernel.
		present bool
	}
	tests := []tc{
		{"a group with the interface file writable", true},
		{"a group whose interface file is absent", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.present {
			//: an absent file reports the fallback signal, never a write fault.
			err := missingGroup(t).Kill()
			if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
				t.Fatalf("Kill on an absent interface file = %v, want UNSUPPORTED_PLATFORM", err)
			}
			return
		}
		g := fakeGroup(t)
		if err := g.Kill(); err != nil {
			t.Fatalf("Kill = %v, want nil", err)
		}
		assertOnlyWrote(t, g, killFile, killTrigger)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_Freeze pins quiescing the whole group.
//
// Freezing is what lets a consistent signal sweep or snapshot run: every member
// stops, so nothing forks out from under the caller mid-sweep.
func Test_controlGroup_Freeze(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: whether the group directory exists; an absent one is how the
		//: kernel-too-old branch becomes reachable without an old kernel.
		present bool
	}
	tests := []tc{
		{"a group with the interface file writable", true},
		{"a group whose interface file is absent", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.present {
			//: an absent file reports the fallback signal, never a write fault.
			err := missingGroup(t).Freeze()
			if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
				t.Fatalf("Freeze on an absent interface file = %v, want UNSUPPORTED_PLATFORM", err)
			}
			return
		}
		g := fakeGroup(t)
		if err := g.Freeze(); err != nil {
			t.Fatalf("Freeze = %v, want nil", err)
		}
		assertOnlyWrote(t, g, freezeFile, freezeOn)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_controlGroup_Thaw pins resuming a frozen group.
//
// Thaw writes the opposite value to the same file as Freeze, so a setter that
// wrote the wrong one would leave a service frozen with no error to show for it.
func Test_controlGroup_Thaw(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: whether the group directory exists; an absent one is how the
		//: kernel-too-old branch becomes reachable without an old kernel.
		present bool
	}
	tests := []tc{
		{"a group with the interface file writable", true},
		{"a group whose interface file is absent", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if !c.present {
			//: an absent file reports the fallback signal, never a write fault.
			err := missingGroup(t).Thaw()
			if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
				t.Fatalf("Thaw on an absent interface file = %v, want UNSUPPORTED_PLATFORM", err)
			}
			return
		}
		g := fakeGroup(t)
		if err := g.Thaw(); err != nil {
			t.Fatalf("Thaw = %v, want nil", err)
		}
		assertOnlyWrote(t, g, freezeFile, freezeOff)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_createGroup pins the order of the guards: the name is validated BEFORE
// the hierarchy is probed, so a crafted name is refused identically on a
// delegated host and an unprivileged one.
func Test_createGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		group    string
		root     string
		wantCode errs.Code
	}
	tests := []tc{
		{"a traversing name under a missing root", "../escape", "/nonexistent/root", coreproc.CodeInvalidSpec},
		{"an empty name under a missing root", "", "/nonexistent/root", coreproc.CodeInvalidSpec},
		{"a valid name under a missing root", "ok", "/nonexistent/root", coreproc.CodeCgroupUnavailable},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := createGroup(c.group, WithRoot(c.root))
		if g != nil {
			t.Fatalf("createGroup(%q) returned a handle", c.group)
		}
		if !errs.HasCode(err, c.wantCode) {
			t.Fatalf("createGroup(%q) = %v, want code %v", c.group, err, c.wantCode)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: a root that exists and carries the controllers marker takes the mkdir
	//: path, which is where a real delegated hierarchy diverges from a fake one.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, controllersFile), []byte("cpu memory\n"), 0o600); err != nil {
		t.Fatalf("seeding the controllers marker: %v", err)
	}
	g, err := createGroup("made", WithRoot(root))
	if err != nil {
		t.Fatalf("createGroup under a seeded root = %v, want nil", err)
	}
	//: the directory must exist under the root we named, and nowhere else.
	made := filepath.Join(root, "made")
	if _, serr := os.Stat(made); serr != nil {
		t.Fatalf("the group directory is missing: %v", serr)
	}
	if !strings.HasPrefix(made, root+string(os.PathSeparator)) {
		t.Errorf("the group landed at %q, outside the root %q", made, root)
	}
	if derr := g.Delete(); derr != nil {
		t.Errorf("Delete = %v, want nil", derr)
	}
}
