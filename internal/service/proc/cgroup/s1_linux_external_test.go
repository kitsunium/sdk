//go:build linux

// The live half of #727 (S1): nested groups, controllers, the typed setters,
// the cgroup.events watch, the members' signal and delegation, on a real
// cgroup v2 hierarchy. Every test works inside a group it makes under a root
// delegated to it, and removes it; with no such root it skips. Nothing is
// written to a group the test did not make, and only processes the test
// started are signalled or moved.
//
// The root is delegatedRoot's (host_linux_external_test.go, which the
// conformance suite of cgroup_external_test.go shares), in this order: the one
// KITSUNIUM_CGROUP_TEST_ROOT names; the mount when Available (root on the
// linux-init lanes); this process's own group when Probe says it is delegated
// (`systemd-run --user --scope -p Delegate=yes go test …`). The first is how a
// lane hands over a root whose children may take controllers: under a
// delegated scope the test process itself sits in the scope, which therefore
// cannot enable a controller for its children (cgroup v2's no-internal-process
// rule).
package cgroup_test

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
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
	procexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// liveTree makes the test's own group under the live root, with ctrls
// enabled for its children, and removes it — and every group the test made
// beneath it — at the end, after killing what is left in it (processes the
// test started, nothing else can be there).
func liveTree(t *testing.T, ctrls ...coreproc.Controller) string {
	t.Helper()
	root := liveRoot(t)
	name := "sdk-s1-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	g, err := cgroup.Create(name, cgroup.WithRoot(root))
	if err != nil {
		t.Fatalf("making the test's group under %s: %v", root, err)
	}
	tree := g.(coreproc.GroupTree)
	t.Cleanup(func() { removeTree(t, tree.Path()) })
	if len(ctrls) > 0 {
		if err := tree.EnableControllers(ctrls...); err != nil {
			t.Fatalf("EnableControllers%v on the test's group: %v", ctrls, err)
		}
	}
	return tree.Path()
}

// removeTree kills what is left under dir and removes its groups, deepest
// first. dir is a group the test made.
func removeTree(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "cgroup.kill"), []byte("1"), 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Logf("cgroup.kill of %s: %v", dir, err)
	}
	var groups []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			groups = append(groups, path)
		}
		return nil
	})
	deadline := time.Now().Add(5 * time.Second)
	for i := len(groups) - 1; i >= 0; i-- {
		for {
			err := os.Remove(groups[i])
			if err == nil || errors.Is(err, os.ErrNotExist) {
				break
			}
			if time.Now().After(deadline) {
				t.Errorf("removing %s: %v", groups[i], err)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// offeredIn reads the controllers dir offers its children.
func offeredIn(t *testing.T, dir string) []string {
	t.Helper()
	listed, err := os.ReadFile(filepath.Join(dir, "cgroup.controllers"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(listed))
}

// sleeper starts a child of this test that sleeps, and kills it at the end.
func sleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Skipf("no sleep(1): %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	return child
}

// TestLiveNestedGroups pins #727's first criterion on a delegated root:
// "a/b" makes the tree, "../x" and an absolute path are refused.
func TestLiveNestedGroups(t *testing.T) {
	tree := liveTree(t)
	g, err := cgroup.Create("a/b", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(a/b) = %v", err)
	}
	if info, serr := os.Stat(filepath.Join(tree, "a", "b", "cgroup.events")); serr != nil || info.IsDir() {
		t.Fatalf("a/b is not a group: %v", serr)
	}
	for _, name := range []string{"../x", "/x", "a/../../x"} {
		if _, err := cgroup.Create(name, cgroup.WithRoot(tree)); !errs.HasCode(err, coreproc.CodeInvalidSpec) {
			t.Errorf("Create(%q) = %v, want INVALID_SPEC", name, err)
		}
	}
	if err := g.Delete(); err != nil {
		t.Fatalf("Delete = %v", err)
	}
}

// TestLiveControllers pins #727's subtree_control criterion: each of io,
// cpuset, hugetlb, memory, cpu and pids is enabled for a new group when the
// test's group offers it — the group then has its files — and is refused
// typed when it does not.
func TestLiveControllers(t *testing.T) {
	tree := liveTree(t)
	offered := offeredIn(t, tree)
	for _, c := range []coreproc.Controller{coreproc.ControllerIO, coreproc.ControllerCPUSet, coreproc.ControllerHugeTLB, coreproc.ControllerMemory, coreproc.ControllerCPU, coreproc.ControllerPids} {
		g, err := cgroup.Create("c/"+c.String(), cgroup.WithRoot(tree), cgroup.WithControllers(c))
		if !slices.Contains(offered, c.String()) {
			if !errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) {
				t.Errorf("%s, not offered: %v, want CGROUP_CONTROLLER_ABSENT", c, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s, offered: %v", c, err)
			continue
		}
		entries, _ := os.ReadDir(g.(coreproc.GroupTree).Path())
		if !slices.ContainsFunc(entries, func(e os.DirEntry) bool { return strings.HasPrefix(e.Name(), c.String()+".") }) {
			t.Errorf("%s enabled, but the group has none of its files", c)
		}
		t.Logf("%s enabled", c)
	}
}

// TestLiveSetters pins #727's second criterion: each setter whose controller
// the test's group offers writes its file, read back; the others are logged.
func TestLiveSetters(t *testing.T) {
	tree := liveTree(t)
	var ctrls []coreproc.Controller
	offered := offeredIn(t, tree)
	for _, c := range []coreproc.Controller{coreproc.ControllerCPU, coreproc.ControllerMemory, coreproc.ControllerCPUSet, coreproc.ControllerIO} {
		if slices.Contains(offered, c.String()) {
			ctrls = append(ctrls, c)
		}
	}
	g, err := cgroup.Create("tuned", cgroup.WithRoot(tree), cgroup.WithControllers(ctrls...))
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	tuner := g.(coreproc.GroupTuner)
	dir := g.(coreproc.GroupTree).Path()
	if _, serr := os.Stat(filepath.Join(dir, "cpu.max")); serr == nil {
		if err := g.SetCPUMax(50_000, 100_000); err != nil {
			t.Fatalf("SetCPUMax = %v", err)
		}
	}
	rows := []struct {
		file string
		set  func() error
		want string
	}{
		{"memory.min", func() error { return tuner.SetMemoryMin(16 << 20) }, "16777216"},
		{"memory.low", func() error { return tuner.SetMemoryLow(32 << 20) }, "33554432"},
		{"memory.high", func() error { return tuner.SetMemoryHigh(128 << 20) }, "134217728"},
		{"memory.swap.max", func() error { return tuner.SetMemorySwapMax(0) }, "0"},
		{"memory.zswap.max", func() error { return tuner.SetMemoryZswapMax(-1) }, "max"},
		{"memory.zswap.writeback", func() error { return tuner.SetMemoryZswapWriteback(false) }, "0"},
		{"memory.oom.group", func() error { return tuner.SetMemoryOOMGroup(true) }, "1"},
		{"cpu.weight", func() error { return tuner.SetCPUWeight(250) }, "250"},
		{"cpu.max.burst", func() error { return tuner.SetCPUMaxBurst(time.Millisecond) }, "1000"},
		{"cpuset.cpus", func() error { return tuner.SetCPUSetCPUs([]int{0}) }, "0"},
		{"cpuset.mems", func() error { return tuner.SetCPUSetMems([]int{0}) }, "0"},
		{"io.weight", func() error { return tuner.SetIOWeight(500) }, "default 500"},
	}
	for _, row := range rows {
		if _, serr := os.Stat(filepath.Join(dir, row.file)); serr != nil {
			t.Logf("%s: not here (%v)", row.file, serr)
			continue
		}
		if err := row.set(); err != nil {
			t.Errorf("%s: %v", row.file, err)
			continue
		}
		got, rerr := os.ReadFile(filepath.Join(dir, row.file))
		if rerr != nil || strings.TrimSpace(string(got)) != row.want {
			t.Errorf("%s reads %q, %v; want %q", row.file, got, rerr, row.want)
		}
	}
}

// TestLiveWatch pins #727's watch criterion: a group holding a child of the
// test reports populated, then frozen=1 after Freeze, survives a burst of
// 100 freeze/thaw writes, reports populated=0 once the child is gone, closes
// the channel when the group is removed (the parent's IN_DELETE: cgroupfs
// raises no IN_IGNORED for the group's files), and leaves no goroutine
// behind; a cancelled context closes the channel.
func TestLiveWatch(t *testing.T) {
	tree := liveTree(t)
	g, err := cgroup.Create("watched", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	child := sleeper(t)
	if err := g.Add(child.Process.Pid); err != nil {
		t.Fatalf("Add = %v", err)
	}
	before := runtime.NumGoroutine()
	ch, err := g.(coreproc.GroupWatcher).Watch(t.Context())
	if err != nil {
		t.Fatalf("Watch = %v", err)
	}
	if first := <-ch; !first.Populated || first.Frozen {
		t.Fatalf("first value %+v", first)
	}
	if err := g.Freeze(); err != nil {
		t.Fatalf("Freeze = %v", err)
	}
	awaitState(t, ch, "frozen=1", func(v coreproc.GroupEventsValue) bool { return v.Frozen })
	for range 50 {
		if err := g.Thaw(); err != nil {
			t.Fatalf("Thaw = %v", err)
		}
		if err := g.Freeze(); err != nil {
			t.Fatalf("Freeze = %v", err)
		}
	}
	if err := g.Thaw(); err != nil {
		t.Fatalf("Thaw = %v", err)
	}
	awaitState(t, ch, "thawed after the burst", func(v coreproc.GroupEventsValue) bool { return !v.Frozen && v.Populated })
	_ = child.Process.Kill()
	_ = child.Wait()
	awaitState(t, ch, "populated=0", func(v coreproc.GroupEventsValue) bool { return !v.Populated })
	if err := g.Delete(); err != nil {
		t.Fatalf("Delete = %v", err)
	}
	awaitClosed(t, ch)
	settle(t, before)

	other, err := cgroup.Create("cancelled", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	ch, err = other.(coreproc.GroupWatcher).Watch(ctx)
	if err != nil {
		t.Fatalf("Watch = %v", err)
	}
	<-ch
	cancel()
	awaitClosed(t, ch)
	settle(t, before)
}

// TestLiveSignalAndDelegate pins the members' signal on a real group — two
// children of the test, one in a group beneath, are members, this process is
// not, and SIGTERM ends both —, Delegate to the test's own ids, and
// DeleteTree: refused while a group beneath holds a child of the test, then
// the whole subtree gone.
func TestLiveSignalAndDelegate(t *testing.T) {
	tree := liveTree(t)
	g, err := cgroup.Create("signalled", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create = %v", err)
	}
	sub, err := cgroup.Create("signalled/sub", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(sub) = %v", err)
	}
	children := []*exec.Cmd{sleeper(t), sleeper(t)}
	if err := g.Add(children[0].Process.Pid); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if err := sub.Add(children[1].Process.Pid); err != nil {
		t.Fatalf("Add(sub) = %v", err)
	}
	signaler := g.(coreproc.GroupSignaler)
	for _, c := range children {
		if in, err := signaler.Contains(c.Process.Pid); err != nil || !in {
			t.Fatalf("Contains(%d) = %v, %v", c.Process.Pid, in, err)
		}
	}
	if in, err := signaler.Contains(os.Getpid()); err != nil || in {
		t.Fatalf("Contains(this process) = %v, %v", in, err)
	}
	if err := signaler.Signal(coreproc.Signal(syscall.SIGTERM)); err != nil {
		t.Fatalf("Signal = %v", err)
	}
	for _, c := range children {
		var exit *exec.ExitError
		if err := c.Wait(); !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Errorf("child %d: %v, want SIGTERM", c.Process.Pid, err)
		}
	}
	if err := g.(coreproc.GroupTree).Delegate(os.Getuid(), os.Getgid()); err != nil {
		t.Fatalf("Delegate = %v", err)
	}
	unit, err := cgroup.Create("unit", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(unit) = %v", err)
	}
	if _, err := cgroup.Create("unit/made/by/it", cgroup.WithRoot(tree)); err != nil {
		t.Fatalf("Create(unit/made/by/it) = %v", err)
	}
	held, err := cgroup.Create("unit/held", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(unit/held) = %v", err)
	}
	child := sleeper(t)
	if err := held.Add(child.Process.Pid); err != nil {
		t.Fatalf("Add = %v", err)
	}
	if err := unit.(coreproc.GroupTree).DeleteTree(); !errs.HasCode(err, coreproc.CodeCgroupDeleteFailed) {
		t.Fatalf("DeleteTree of a populated subtree = %v, want CGROUP_DELETE_FAILED", err)
	}
	_ = child.Process.Kill()
	_ = child.Wait()
	if err := unit.(coreproc.GroupTree).DeleteTree(); err != nil {
		t.Fatalf("DeleteTree = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "unit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the unit's group is still there: %v", err)
	}
}

// TestLiveSpawnIntoTheTreesPath pins the junction of #727 with the spawn's
// placement (ADR 0217, #929): the directory GroupTree.Path names is the
// Spec.CgroupPath exec.Start places a child into before it runs, and the
// group then holds it — Contains says so — and, as the unit's CgroupKill
// proc/service asks for (S0g, #734), Kill ends it.
func TestLiveSpawnIntoTheTreesPath(t *testing.T) {
	tree := liveTree(t)
	g, err := cgroup.Create("unit/main", cgroup.WithRoot(tree))
	if err != nil {
		t.Fatalf("Create(unit/main) = %v", err)
	}
	path := g.(coreproc.GroupTree).Path()
	if path != filepath.Join(tree, "unit", "main") {
		t.Fatalf("Path = %q, want %q", path, filepath.Join(tree, "unit", "main"))
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("no sleep(1): %v", err)
	}
	child, err := procexec.Start(t.Context(), coreproc.Spec{Path: sleep, Args: []string{"sleep", "30"}, CgroupPath: path})
	if err != nil {
		t.Fatalf("Start into %s = %v", path, err)
	}
	t.Cleanup(func() {
		_ = child.Signal(coreproc.Signal(syscall.SIGKILL))
		_, _ = child.Wait()
	})
	unit := g.(coresvc.CgroupKill)
	if in, err := unit.Contains(child.PID()); err != nil || !in {
		t.Fatalf("Contains(%d) = %v, %v: the child is not in the group it was spawned into", child.PID(), in, err)
	}
	if err := unit.Kill(); err != nil {
		t.Fatalf("Kill = %v", err)
	}
	exit, _ := child.Wait()
	if !exit.Signaled || exit.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("the child ended with %+v, want SIGKILL from the group's Kill", exit)
	}
}

// awaitState reads ch until want holds, failing after five seconds or on a
// closed channel.
func awaitState(t *testing.T, ch <-chan coreproc.GroupEventsValue, what string, want func(coreproc.GroupEventsValue) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case v, ok := <-ch:
			if !ok {
				t.Fatalf("the channel closed before %s", what)
			}
			if want(v) {
				return
			}
		case <-deadline:
			t.Fatalf("no %s within five seconds", what)
		}
	}
}

// awaitClosed reads ch until it closes, failing after five seconds.
func awaitClosed(t *testing.T, ch <-chan coreproc.GroupEventsValue) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the channel did not close within five seconds")
		}
	}
}

// settle waits for the goroutine count to fall back to before.
func settle(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > before {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, %d before", runtime.NumGoroutine(), before)
		}
		time.Sleep(time.Millisecond)
	}
}
