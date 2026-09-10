//go:build linux

// Package cgroup — the measurement that could NOT be completed as intended, and
// says so.
//
// Every verb this package exists for (Create, Add, Set*Max, Kill, Freeze,
// Delete) is a mkdir(2) or a write(2) against cgroupfs, and cgroupfs is where
// the cost is: the kernel parses the value in a controller-specific handler and
// applies it to a live hierarchy. Measuring that needs a DELEGATED cgroup v2
// subtree. This container has cgroup v2 mounted read-write with every controller
// enabled, and grants the caller no delegation at all — mkdir is EACCES both at
// the root and inside this process's own scope. So the live paths are not
// measurable here, `Available()` returns false, and the benchmark does NOT
// pretend otherwise: it measures the probe honestly, measures the pure path and
// value construction, and isolates the SDK's share of a controller write against
// a plain filesystem, clearly labelled as NOT cgroupfs.
//
// It is an INTERNAL benchmark because validateName, withinRoot and maxOrValue —
// the whole pure half — are unexported.
package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
)

// benchShm is the memory filesystem preferred for the plain-filesystem rows:
// both it and TMPDIR are tmpfs here, but on a shared build machine TMPDIR is
// contended by every other job and the resulting write times measure that
// contention rather than this package.
const benchShm string = "/dev/shm"

// Sinks defeat dead-code elimination on the value-returning helpers.
var (
	errSink   error
	boolSink  bool
	strSink   string
	groupSink coreproc.Group
)

// ── the probe ────────────────────────────────────────────────────────────────

// BenchmarkAvailable is an honest measurement of an honest probe: a stat(2) of
// the cgroup.controllers marker followed by a real MkdirTemp attempt under the
// mount, because "is the hierarchy delegated to me" cannot be answered without
// trying. On this container the mkdir is refused, so this row is the cost of the
// DENIED path — which is the path every unprivileged container takes, and the
// reason the number matters: a caller who calls Available() per spawn is paying
// two syscalls to be told "no" again.
func BenchmarkAvailable(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = Available()
	}
	b.StopTimer()
	b.Logf("Available() == %v on this host", boolSink)
}

// ── the pure half: name validation and value formatting ──────────────────────

func BenchmarkValidateName_Valid(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = validateName("payload.scope")
	}
}

// BenchmarkValidateName_Traversal is the refusal that keeps a crafted name from
// reaching filepath.Join. It is measured because a guard whose cost depends on
// the attacker's input is a guard worth pricing.
func BenchmarkValidateName_Traversal(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = validateName("../../escape")
	}
}

func BenchmarkWithinRoot_Inside(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		boolSink = withinRoot(mountRoot, filepath.Join(mountRoot, "payload"))
	}
}

func BenchmarkMaxOrValue_Number(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = maxOrValue(1 << 30)
	}
}

// BenchmarkMaxOrValue_Unlimited is the SDK's negative-means-"max" convention,
// which returns a constant and must therefore be free.
func BenchmarkMaxOrValue_Unlimited(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = maxOrValue(-1)
	}
}

func BenchmarkApplyOptions_None(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = applyOptions(nil).root
	}
}

func BenchmarkApplyOptions_WithRoot(b *testing.B) {
	opts := []Option{WithRoot("/sys/fs/cgroup/delegated.slice")}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		strSink = applyOptions(opts).root
	}
}

// ── the SDK's share of a controller write, against a plain filesystem ────────

// BenchmarkCreate_Unavailable is the refusal a non-delegated host produces:
// applyOptions, validateName, one stat(2) of the controllers marker, and the
// typed CgroupUnavailable wrap. It is the whole of Create on a host like this
// one, so it is the only Create number this container can honestly produce.
func BenchmarkCreate_Unavailable(b *testing.B) {
	if Available() {
		b.Skip("this host DOES delegate cgroup v2 — re-run the live rows instead")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		g, err := Create("kitsu-bench")
		groupSink, errSink = g, err
	}
}

// BenchmarkWriteController_TmpfsNotCgroupfs measures the SDK's own share of a
// Set*Max call — filepath.Join, the decimal rendering, and one os.WriteFile —
// by pointing a controlGroup at a PLAIN DIRECTORY.
//
// Read the name. This is NOT a cgroup write and the number is NOT what
// SetMemoryMax costs against cgroupfs: the kernel's memory-controller handler
// does real work that a tmpfs write does not, and none of it is here. What the
// row does establish is the FLOOR — the part of a controller write that belongs
// to this package rather than to the kernel — so that when someone runs this
// suite on a host with delegation, the difference between that number and this
// one is attributable to cgroupfs and to nothing else.
func BenchmarkWriteController_TmpfsNotCgroupfs(b *testing.B) {
	dir := benchPlainDir(b)
	g := &controlGroup{dir: dir}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = g.SetMemoryMax(1 << 30)
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("SetMemoryMax against a plain directory: %v", errSink)
	}
}

// BenchmarkAdd_TmpfsNotCgroupfs is the same isolation for Add, whose only extra
// work over SetMemoryMax is strconv.Itoa of the pid. Same caveat, in full: a
// plain file is not cgroup.procs and the kernel does not move a process here.
func BenchmarkAdd_TmpfsNotCgroupfs(b *testing.B) {
	dir := benchPlainDir(b)
	g := &controlGroup{dir: dir}
	pid := os.Getpid()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = g.Add(pid)
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("Add against a plain directory: %v", errSink)
	}
}

// benchPlainDir makes a private directory outside any cgroup hierarchy. It is
// created by hand rather than with b.TempDir() because TMPDIR here carries a
// POSIX ACL that would make the directory group-writable, and Mkdir's mode is
// masked by umask — hence the explicit Chmod after it.
//
// /dev/shm is preferred over TMPDIR when it exists: both are tmpfs, but on a
// shared build machine TMPDIR is contended by every other job and the resulting
// write times are that contention, not the SDK's floor.
func benchPlainDir(b *testing.B) string {
	b.Helper()
	parent := os.TempDir()
	if info, err := os.Stat(benchShm); err == nil && info.IsDir() {
		parent = benchShm
	}
	dir := filepath.Join(parent, "kitsu-cgroup-bench-"+strconv.Itoa(os.Getpid()))
	if err := os.Mkdir(dir, 0o700); err != nil {
		b.Skipf("cannot create a private directory: %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		b.Skipf("cannot narrow the private directory: %v", err)
	}
	b.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			b.Logf("RemoveAll: %v", err)
		}
	})
	b.Logf("plain-filesystem rows are running under %s", parent)
	return dir
}

// BenchmarkRawWriteFile_Baseline is os.WriteFile with the same payload into the
// same directory, with no SDK code in the frame at all. Subtracting it from
// BenchmarkWriteController_TmpfsNotCgroupfs is the only way to read that row:
// whatever is left is filepath.Join plus the decimal rendering, and nothing
// else.
func BenchmarkRawWriteFile_Baseline(b *testing.B) {
	path := filepath.Join(benchPlainDir(b), "memory.max")
	payload := []byte(strconv.FormatInt(1<<30, decimalBase))
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		errSink = os.WriteFile(path, payload, controllerPerm)
	}
	b.StopTimer()
	if errSink != nil {
		b.Fatalf("os.WriteFile: %v", errSink)
	}
}
