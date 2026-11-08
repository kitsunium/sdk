//go:build linux

// White-box tests of #727 (S1): nested groups, controllers, the typed
// setters, delegation, the cgroup.events watch and the members' signal, each
// on a fake cgroup filesystem under t.TempDir() — a control group is a
// directory and every verb a read or a write in it, so the same code runs
// without a delegated hierarchy. The live half is s1_linux_external_test.go.
package cgroup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// A Linux group is, as it is, what proc/service asks of a unit's control
// group (S0g, #734): its CgroupKill (Kill, Signal, Contains), its
// CgroupMembers (Members, the subtree) and its CgroupUnit (the cgroup modes).
// The engine's default adapter over this package is that type assertion.
var (
	_ coresvc.CgroupKill    = (*controlGroup)(nil)
	_ coresvc.CgroupMembers = (*controlGroup)(nil)
	_ coresvc.CgroupUnit    = (*controlGroup)(nil)
)

// fakeRoot is a directory carrying the cgroup v2 marker, offering offered.
func fakeRoot(t *testing.T, offered string) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, controllersFile), offered)
	return root
}

// writeFile writes content to path or fails the test.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// readFile returns path's content, or "<absent>".
func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "<absent>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

// emptyDir fails the test when dir holds anything: the witness of an escape.
func emptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("the witness %s outside the root holds %v: something escaped", dir, entries)
	}
}

// TestCreateMakesANestedGroupUnderTheRoot pins #727's first criterion on a
// fake root: "a/b/c" makes the missing groups on the way, a second group
// beside the first keeps them, the same name twice is refused, and every
// escape — "..", an absolute path, a link out of the root or inside it —
// is INVALID_SPEC with nothing written in a witness directory outside.
func TestCreateMakesANestedGroupUnderTheRoot(t *testing.T) {
	t.Parallel()
	root := fakeRoot(t, "cpu memory pids\n")
	g, err := createGroup("a/b/c", WithRoot(root))
	if err != nil {
		t.Fatalf("createGroup(a/b/c) = %v", err)
	}
	if got := g.(coreproc.GroupTree).Path(); got != filepath.Join(root, "a", "b", "c") {
		t.Fatalf("Path = %q", got)
	}
	if _, err := createGroup("a/b/d", WithRoot(root)); err != nil {
		t.Fatalf("a sibling under the kept groups: %v", err)
	}
	if _, err := createGroup("a/b/c", WithRoot(root)); !errs.HasCode(err, coreproc.CodeCgroupCreateFailed) {
		t.Fatalf("the same group twice = %v, want CGROUP_CREATE_FAILED", err)
	}
	witness := t.TempDir()
	if err := os.Symlink(witness, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "inner"), filepath.Join(root, "in")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x", "/abs", "a/../../x", "out/x", "out/x/y", "in/x", "out"} {
		if _, err := createGroup(name, WithRoot(root)); !errs.HasCode(err, coreproc.CodeInvalidSpec) && !errs.HasCode(err, coreproc.CodeCgroupCreateFailed) {
			t.Errorf("createGroup(%q) = %v, want a refusal", name, err)
		}
	}
	for _, name := range []string{"../x", "/abs", "out/x", "in/x"} {
		if _, err := createGroup(name, WithRoot(root)); !errs.HasCode(err, coreproc.CodeInvalidSpec) {
			t.Errorf("createGroup(%q) = %v, want INVALID_SPEC", name, err)
		}
	}
	emptyDir(t, witness)
	emptyDir(t, filepath.Join(root, "inner"))
}

// TestCreateEnablesControllersTopDown pins that WithControllers writes
// "+memory +pids" to the root's cgroup.subtree_control and to every group on
// the way, never to the new group itself, and that a controller the root
// does not offer — io, cpuset, hugetlb on a delegated user scope — or a
// value no constant holds is refused before anything is made.
func TestCreateEnablesControllersTopDown(t *testing.T) {
	t.Parallel()
	root := fakeRoot(t, "cpu memory pids\n")
	ctrls := WithControllers(coreproc.ControllerMemory, coreproc.ControllerPids)
	if _, err := createGroup("a/b", WithRoot(root), ctrls); err != nil {
		t.Fatalf("createGroup = %v", err)
	}
	for _, dir := range []string{root, filepath.Join(root, "a")} {
		if got := readFile(t, filepath.Join(dir, subtreeControlFile)); got != "+memory +pids" {
			t.Errorf("%s/cgroup.subtree_control = %q, want +memory +pids", dir, got)
		}
	}
	if got := readFile(t, filepath.Join(root, "a", "b", subtreeControlFile)); got != "<absent>" {
		t.Errorf("the new group's own subtree_control = %q, want none", got)
	}
	for _, c := range []coreproc.Controller{coreproc.ControllerIO, coreproc.ControllerCPUSet, coreproc.ControllerHugeTLB} {
		bare := fakeRoot(t, "cpu memory pids\n")
		_, err := createGroup("x", WithRoot(bare), WithControllers(coreproc.ControllerCPU, c))
		if !errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) || fieldValue(err, "controller") != c.String() {
			t.Errorf("WithControllers(%s) = %v, want CGROUP_CONTROLLER_ABSENT naming it", c, err)
		}
		if got := readFile(t, filepath.Join(bare, subtreeControlFile)); got != "<absent>" {
			t.Errorf("a refused controller wrote %q", got)
		}
		if _, serr := os.Stat(filepath.Join(bare, "x")); !errors.Is(serr, os.ErrNotExist) {
			t.Errorf("a refused controller made the group: %v", serr)
		}
	}
	for _, c := range []coreproc.Controller{0, coreproc.ControllerMisc + 1, 255} {
		if _, err := createGroup("y", WithRoot(root), WithControllers(c)); !errs.HasCode(err, coreproc.CodeInvalidSpec) {
			t.Errorf("WithControllers(%v) = %v, want INVALID_SPEC", c, err)
		}
	}
}

// TestCreateUndoesWhatAFailedCreateMade pins the undo: a nested create whose
// last mkdir fails leaves none of the groups it made on the way.
func TestCreateUndoesWhatAFailedCreateMade(t *testing.T) {
	t.Parallel()
	root := fakeRoot(t, "cpu\n")
	long := strings.Repeat("x", 300)
	if _, err := createGroup("made/"+long, WithRoot(root)); !errs.HasCode(err, coreproc.CodeCgroupCreateFailed) {
		t.Fatalf("an over-long last name = %v, want CGROUP_CREATE_FAILED", err)
	}
	if _, err := os.Stat(filepath.Join(root, "made")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the group made on the way is still there: %v", err)
	}
}

// TestEnableControllersOnAGroup pins GroupTree.EnableControllers: what the
// group's cgroup.controllers lists is written, what it does not is refused
// before the write, and an empty call writes nothing.
func TestEnableControllersOnAGroup(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	writeFile(t, filepath.Join(g.dir, controllersFile), "cpu memory\n")
	if err := g.EnableControllers(); err != nil || readFile(t, filepath.Join(g.dir, subtreeControlFile)) != "<absent>" {
		t.Fatalf("EnableControllers() = %v, want a no-op", err)
	}
	if err := g.EnableControllers(coreproc.ControllerCPU, coreproc.ControllerMemory); err != nil {
		t.Fatalf("EnableControllers(cpu, memory) = %v", err)
	}
	if got := readFile(t, filepath.Join(g.dir, subtreeControlFile)); got != "+cpu +memory" {
		t.Fatalf("cgroup.subtree_control = %q", got)
	}
	if err := g.EnableControllers(coreproc.ControllerIO); !errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) {
		t.Fatalf("EnableControllers(io) = %v, want CGROUP_CONTROLLER_ABSENT", err)
	}
	if err := g.EnableControllers(coreproc.ControllerMemory, 0); !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("EnableControllers(memory, 0) = %v, want INVALID_SPEC", err)
	}
	if err := missingGroup(t).EnableControllers(coreproc.ControllerCPU); !errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) {
		t.Fatalf("EnableControllers on a missing group = %v", err)
	}
}

// TestEnableInMapsTheKernelsRefusals pins enableIn's classification: ENOENT
// (a controller the kernel does not offer there) is
// CGROUP_CONTROLLER_ABSENT, EBUSY (a group holding a process) is
// CGROUP_WRITE_FAILED.
func TestEnableInMapsTheKernelsRefusals(t *testing.T) {
	t.Parallel()
	refuse := func(errno syscall.Errno) func(string, []byte, os.FileMode) error {
		return func(name string, _ []byte, _ os.FileMode) error {
			return &os.PathError{Op: "write", Path: name, Err: errno}
		}
	}
	ctrls := []coreproc.Controller{coreproc.ControllerIO}
	if err := enableIn(refuse(syscall.ENOENT), ".", "/g", ctrls); !errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) {
		t.Errorf("ENOENT = %v", err)
	}
	if err := enableIn(refuse(syscall.EBUSY), ".", "/g", ctrls); !errs.HasCode(err, coreproc.CodeCgroupWriteFailed) {
		t.Errorf("EBUSY = %v", err)
	}
}

// TestEverySetterWritesItsFile pins each GroupTuner setter's file and value
// on a fake group, read back.
func TestEverySetterWritesItsFile(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	rows := []struct {
		set  func() error
		file string
		want string
	}{
		{func() error { return g.SetMemoryMin(64 << 20) }, "memory.min", "67108864"},
		{func() error { return g.SetMemoryLow(-1) }, "memory.low", "max"},
		{func() error { return g.SetMemoryHigh(128 << 20) }, "memory.high", "134217728"},
		{func() error { return g.SetMemorySwapMax(0) }, "memory.swap.max", "0"},
		{func() error { return g.SetMemoryZswapMax(-1) }, "memory.zswap.max", "max"},
		{func() error { return g.SetMemoryZswapWriteback(false) }, "memory.zswap.writeback", "0"},
		{func() error { return g.SetMemoryOOMGroup(true) }, "memory.oom.group", "1"},
		{func() error { return g.SetCPUWeight(250) }, "cpu.weight", "250"},
		{func() error { return g.SetCPUMaxBurst(1500 * time.Microsecond) }, "cpu.max.burst", "1500"},
		{func() error { return g.SetCPUSetCPUs([]int{3, 0, 1, 2, 8, 8}) }, "cpuset.cpus", "0-3,8"},
		{func() error { return g.SetCPUSetMems(nil) }, "cpuset.mems", ""},
		{func() error { return g.SetIOWeight(500) }, "io.weight", "default 500"},
		{func() error { return g.SetIOLatency(8, 16, 2*time.Millisecond) }, "io.latency", "8:16 target=2000"},
	}
	for _, row := range rows {
		if err := row.set(); err != nil {
			t.Errorf("%s: %v", row.file, err)
			continue
		}
		if got := readFile(t, filepath.Join(g.dir, row.file)); got != row.want {
			t.Errorf("%s = %q, want %q", row.file, got, row.want)
		}
	}
	if err := g.SetIOLatency(8, 16, 0); err != nil || readFile(t, filepath.Join(g.dir, "io.latency")) != "8:16 target=max" {
		t.Errorf("a zero latency target = %v, %q; want the device's line removed", err, readFile(t, filepath.Join(g.dir, "io.latency")))
	}
	if err := g.SetIOLatency(8, 16, time.Nanosecond); err != nil || readFile(t, filepath.Join(g.dir, "io.latency")) != "8:16 target=1" {
		t.Errorf("a sub-microsecond target = %v, want one microsecond, never a removal", err)
	}
	if err := missingGroup(t).SetCPUWeight(100); !errs.HasCode(err, coreproc.CodeCgroupWriteFailed) {
		t.Errorf("a setter on a missing group = %v, want CGROUP_WRITE_FAILED", err)
	}
}

// TestSettersRefuseOutOfRangeValues pins that a value outside the kernel's
// range is INVALID_SPEC and writes nothing.
func TestSettersRefuseOutOfRangeValues(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	refused := map[string]func() error{
		"cpu.weight":    func() error { return g.SetCPUWeight(0) },
		"io.weight":     func() error { return g.SetIOWeight(10001) },
		"cpu.max.burst": func() error { return g.SetCPUMaxBurst(-time.Microsecond) },
		"cpuset.cpus":   func() error { return g.SetCPUSetCPUs([]int{0, -1}) },
		"cpuset.mems":   func() error { return g.SetCPUSetMems([]int{-2}) },
		"io.latency":    func() error { return g.SetIOLatency(8, 0, -time.Second) },
	}
	for file, set := range refused {
		err := set()
		if !errs.HasCode(err, coreproc.CodeInvalidSpec) || fieldValue(err, "file") != file {
			t.Errorf("%s: %v, want INVALID_SPEC naming the file", file, err)
		}
		if got := readFile(t, filepath.Join(g.dir, file)); got != "<absent>" {
			t.Errorf("%s was written: %q", file, got)
		}
	}
}

// Test_listOf pins the kernel's list form.
func Test_listOf(t *testing.T) {
	t.Parallel()
	rows := []struct {
		in   []int
		want string
		ok   bool
	}{
		{nil, "", true},
		{[]int{0}, "0", true},
		{[]int{0, 1}, "0-1", true},
		{[]int{5, 3, 4, 9, 11, 10, 0}, "0,3-5,9-11", true},
		{[]int{2, 2, 2}, "2", true},
		{[]int{1, -1}, "1,-1", false},
	}
	for _, row := range rows {
		got, ok := listOf(row.in)
		if got != row.want || ok != row.ok {
			t.Errorf("listOf(%v) = %q, %v; want %q, %v", row.in, got, ok, row.want, row.ok)
		}
	}
}

// TestDelegateHandsTheKernelsFilesOver pins Delegate: the kernel's list read
// as plain names only — a separator or a dot element is never chowned —, the
// documented three where it is absent, the directory before its files, a file
// the group lacks skipped, a refusal typed, and an id below -1 refused.
func TestDelegateHandsTheKernelsFilesOver(t *testing.T) {
	t.Parallel()
	listed := func(content string, err error) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(content), err }
	}
	got := delegationFiles(listed("cgroup.procs\ncgroup.threads\n../escape\nsub/file\n..\nmemory.oom.group\n", nil))
	if !slices.Equal(got, []string{"cgroup.procs", "cgroup.threads", "memory.oom.group"}) {
		t.Errorf("delegationFiles = %v", got)
	}
	fallback := []string{procsFile, threadsFile, subtreeControlFile}
	if got := delegationFiles(listed("", os.ErrNotExist)); !slices.Equal(got, fallback) {
		t.Errorf("no kernel list = %v, want %v", got, fallback)
	}
	if got := delegationFiles(listed("../x\n", nil)); !slices.Equal(got, fallback) {
		t.Errorf("a list with nothing usable = %v, want %v", got, fallback)
	}
	var calls []string
	chown := func(name string, uid, gid int) error {
		calls = append(calls, filepath.Base(name)+":"+strconv.Itoa(uid)+":"+strconv.Itoa(gid))
		if strings.HasSuffix(name, "absent") {
			return &os.PathError{Op: "lchown", Path: name, Err: syscall.ENOENT}
		}
		return nil
	}
	if err := delegateIn("/g/unit", []string{"cgroup.procs", "absent", "cgroup.threads"}, 1000, -1, chown); err != nil {
		t.Fatalf("delegateIn = %v", err)
	}
	if want := []string{"unit:1000:-1", "cgroup.procs:1000:-1", "absent:1000:-1", "cgroup.threads:1000:-1"}; !slices.Equal(calls, want) {
		t.Errorf("chown calls = %v, want %v", calls, want)
	}
	deny := func(string, int, int) error { return syscall.EPERM }
	if err := delegateIn("/g/unit", nil, 1000, 1000, deny); !errs.HasCode(err, coreproc.CodeCgroupWriteFailed) {
		t.Errorf("a refused chown = %v, want CGROUP_WRITE_FAILED", err)
	}
	g := fakeGroup(t)
	writeFile(t, filepath.Join(g.dir, procsFile), "")
	if err := g.Delegate(os.Getuid(), os.Getgid()); err != nil {
		t.Errorf("Delegate to this test's own ids = %v", err)
	}
	if err := g.Delegate(-2, 0); !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Errorf("Delegate(-2, 0) = %v, want INVALID_SPEC", err)
	}
}

// TestSignalTreeReachesEveryNewMemberOnce pins signalTree on a recorded kill:
// a process group (0, -1) and the caller are never signalled, a member is
// signalled once however many passes list it, one forked during a pass is
// found by the next, a member already gone is no failure, a refusal is
// SIGNAL_FAILED, and a tree that never stops forking ends after
// maxSignalPasses passes.
func TestSignalTreeReachesEveryNewMemberOnce(t *testing.T) {
	t.Parallel()
	const self = 4242
	passes := [][]int{{0, -1, self, 10, 11}, {10, 11, 12}, {10, 11, 12}}
	pass := 0
	members := func() ([]int, error) {
		listed := passes[min(pass, len(passes)-1)]
		pass++
		return listed, nil
	}
	var killed []int
	kill := func(pid int) error {
		killed = append(killed, pid)
		if pid == 11 {
			return syscall.ESRCH
		}
		return nil
	}
	if err := signalTree(members, kill, self); err != nil {
		t.Fatalf("signalTree = %v", err)
	}
	if !slices.Equal(killed, []int{10, 11, 12}) || pass != 3 {
		t.Fatalf("killed %v in %d passes, want [10 11 12] in 3", killed, pass)
	}
	refused := func(int) error { return syscall.EPERM }
	if err := signalTree(func() ([]int, error) { return []int{7}, nil }, refused, self); !errs.HasCode(err, coreproc.CodeSignalFailed) {
		t.Fatalf("a refused kill = %v, want SIGNAL_FAILED", err)
	}
	next := 100
	forking := func() ([]int, error) { next++; return []int{next}, nil }
	count := 0
	if err := signalTree(forking, func(int) error { count++; return nil }, self); err != nil || count != maxSignalPasses {
		t.Fatalf("a tree forking forever: %v after %d kills, want %d", err, count, maxSignalPasses)
	}
	gone := func() ([]int, error) { return nil, cgroupReadFailed(os.ErrNotExist, "/g") }
	if err := signalTree(gone, kill, self); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Fatalf("a group gone = %v, want CGROUP_READ_FAILED", err)
	}
}

// TestSignalAndContainsReachTheSubtree pins Signal and Contains on a fake
// group holding two children this test launched, one in a group beneath: both
// are members, this process is not, and SIGTERM ends both.
func TestSignalAndContainsReachTheSubtree(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	sub := filepath.Join(g.dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(g.dir, "link")); err != nil {
		t.Fatal(err)
	}
	children := make([]*exec.Cmd, 2)
	for i := range children {
		children[i] = exec.Command("sleep", "30")
		if err := children[i].Start(); err != nil {
			t.Skipf("no sleep(1): %v", err)
		}
		t.Cleanup(func() { _ = children[i].Process.Kill(); _ = children[i].Wait() })
	}
	writeFile(t, filepath.Join(g.dir, procsFile), strconv.Itoa(children[0].Process.Pid)+"\n")
	writeFile(t, filepath.Join(sub, procsFile), strconv.Itoa(children[1].Process.Pid)+"\n")
	for _, c := range children {
		if in, err := g.Contains(c.Process.Pid); err != nil || !in {
			t.Fatalf("Contains(%d) = %v, %v", c.Process.Pid, in, err)
		}
	}
	if in, err := g.Contains(os.Getpid()); err != nil || in {
		t.Fatalf("Contains(this process) = %v, %v", in, err)
	}
	if err := g.Signal(coreproc.Signal(syscall.SIGTERM)); err != nil {
		t.Fatalf("Signal(SIGTERM) = %v", err)
	}
	for _, c := range children {
		err := c.Wait()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Errorf("child %d ended with %v, want SIGTERM", c.Process.Pid, err)
		}
	}
	if _, err := missingGroup(t).Contains(1); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Errorf("Contains on a missing group = %v", err)
	}
}

// nextValue waits for a value of ch that satisfies want, failing after five
// seconds; it returns false when ch closed first.
func nextValue(t *testing.T, ch <-chan coreproc.GroupEventsValue, want func(coreproc.GroupEventsValue) bool) (coreproc.GroupEventsValue, bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return v, false
			}
			if want(v) {
				return v, true
			}
		case <-deadline:
			t.Fatal("no value came")
		}
	}
}

// settledGoroutines waits for the goroutine count to fall back to at most
// before, failing after five seconds.
func settledGoroutines(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, %d before the watch", runtime.NumGoroutine(), before)
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
}

// lastBeforeClose reads ch until it closes, failing after five seconds, and
// returns the last value it held.
func lastBeforeClose(t *testing.T, ch <-chan coreproc.GroupEventsValue) coreproc.GroupEventsValue {
	t.Helper()
	var last coreproc.GroupEventsValue
	deadline := time.After(5 * time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				return last
			}
			last = v
		case <-deadline:
			t.Fatal("the channel did not close")
		}
	}
}

// TestWatchFollowsAGroupUntilItGoes pins #727's watch on a fake group: the
// first value is the state when the watch began, populated=0 and frozen=1
// and an OOM kill in memory.events each come through, a burst of 500
// writes ends on the last state without the watch blocking on a reader that
// does not read, and removing cgroup.events (IN_IGNORED) closes the channel
// after a last value holding no process, leaving no goroutine behind.
func TestWatchFollowsAGroupUntilItGoes(t *testing.T) {
	g := fakeGroup(t)
	events := filepath.Join(g.dir, eventsFile)
	writeFile(t, events, "populated 1\nfrozen 0\n")
	writeFile(t, filepath.Join(g.dir, memoryEventsFile), "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n")
	before := runtime.NumGoroutine()
	ch, err := g.Watch(t.Context())
	if err != nil {
		t.Fatalf("Watch = %v", err)
	}
	if first := <-ch; !first.Populated || first.Frozen {
		t.Fatalf("first value %+v, want populated", first)
	}
	writeFile(t, events, "populated 1\nfrozen 1\n")
	nextValue(t, ch, func(v coreproc.GroupEventsValue) bool { return v.Frozen })
	writeFile(t, filepath.Join(g.dir, memoryEventsFile), "low 0\nhigh 0\nmax 3\noom 1\noom_kill 2\n")
	nextValue(t, ch, func(v coreproc.GroupEventsValue) bool { return v.MemoryEvents.OOMKill == 2 && v.MemoryEvents.Max == 3 })
	for i := range 500 {
		writeFile(t, events, "populated 1\nfrozen "+strconv.Itoa(i%2)+"\n")
	}
	writeFile(t, events, "populated 0\nfrozen 1\n")
	nextValue(t, ch, func(v coreproc.GroupEventsValue) bool { return !v.Populated && v.Frozen })
	if err := os.Remove(events); err != nil {
		t.Fatal(err)
	}
	if last := lastBeforeClose(t, ch); last.Populated {
		t.Fatalf("last value %+v, want no process", last)
	}
	settledGoroutines(t, before)
}

// TestWatchEndsWithItsContext pins that cancelling ctx closes the channel
// and ends the watch's goroutine, and that a group without cgroup.events
// cannot be watched.
func TestWatchEndsWithItsContext(t *testing.T) {
	g := fakeGroup(t)
	writeFile(t, filepath.Join(g.dir, eventsFile), "populated 0\nfrozen 0\n")
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(t.Context())
	ch, err := g.Watch(ctx)
	if err != nil {
		t.Fatalf("Watch = %v", err)
	}
	<-ch
	cancel()
	if _, open := nextValue(t, ch, func(coreproc.GroupEventsValue) bool { return false }); open {
		t.Fatal("the channel stayed open after ctx ended")
	}
	settledGoroutines(t, before)
	if _, err := missingGroup(t).Watch(t.Context()); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Fatalf("Watch on a missing group = %v, want CGROUP_READ_FAILED", err)
	}
	if _, err := fakeGroup(t).Watch(t.Context()); !errs.HasCode(err, coreproc.CodeCgroupReadFailed) {
		t.Fatalf("Watch on a group without cgroup.events = %v, want CGROUP_READ_FAILED", err)
	}
}

// TestWatchHearsTheGroupLeaveItsParent pins the removal cgroupfs does
// announce: the parent's IN_DELETE or IN_MOVED_FROM naming the group (an
// rmdir of a group raises no IN_IGNORED for a watch on its files). A
// sibling's removal is no change and publishes nothing; the group renamed
// away — even with a new one in its place — ends the watch.
func TestWatchHearsTheGroupLeaveItsParent(t *testing.T) {
	parent := t.TempDir()
	g := &controlGroup{dir: filepath.Join(parent, "group")}
	if err := os.Mkdir(g.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(g.dir, eventsFile), "populated 1\nfrozen 0\n")
	ch, err := g.Watch(t.Context())
	if err != nil {
		t.Fatalf("Watch = %v", err)
	}
	<-ch
	if err := os.Mkdir(filepath.Join(parent, "sibling"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(parent, "sibling")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(g.dir, eventsFile), "populated 0\nfrozen 0\n")
	if v, open := nextValue(t, ch, func(coreproc.GroupEventsValue) bool { return true }); !open || v.Populated {
		t.Fatalf("after a sibling's removal and a change: %+v (open %v), want the change", v, open)
	}
	if err := os.Rename(g.dir, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(g.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(g.dir, eventsFile), "populated 1\nfrozen 0\n")
	if last := lastBeforeClose(t, ch); last.Populated {
		t.Fatalf("after the group left: %+v, want the channel closed on no process", last)
	}
}

// TestDeleteTreeRemovesWhatWasMadeBeneath pins GroupTree.DeleteTree on a fake
// group: the groups a delegated unit made beneath it go, deepest first, with
// the group; a link beneath is never followed — its target, a witness outside,
// keeps its content — and the group it stands in is refused, as is a group
// still holding something.
func TestDeleteTreeRemovesWhatWasMadeBeneath(t *testing.T) {
	t.Parallel()
	g := fakeGroup(t)
	for _, dir := range []string{"a/b/c", "a/d", "e"} {
		if err := os.MkdirAll(filepath.Join(g.dir, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.DeleteTree(); err != nil {
		t.Fatalf("DeleteTree = %v", err)
	}
	if _, err := os.Stat(g.dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the group is still there: %v", err)
	}
	if err := g.DeleteTree(); !errs.HasCode(err, coreproc.CodeCgroupDeleteFailed) {
		t.Fatalf("DeleteTree of a group gone = %v, want CGROUP_DELETE_FAILED", err)
	}
	linked := fakeGroup(t)
	witness := t.TempDir()
	writeFile(t, filepath.Join(witness, "kept"), "kept")
	if err := os.Mkdir(filepath.Join(linked.dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(witness, filepath.Join(linked.dir, "sub", "out")); err != nil {
		t.Fatal(err)
	}
	err := linked.DeleteTree()
	if !errs.HasCode(err, coreproc.CodeCgroupDeleteFailed) || fieldValue(err, "dir") != filepath.Join(linked.dir, "sub") {
		t.Fatalf("DeleteTree over a link = %v, want CGROUP_DELETE_FAILED naming sub", err)
	}
	if got := readFile(t, filepath.Join(witness, "kept")); got != "kept" {
		t.Fatalf("the link's target lost its content: %q", got)
	}
}
