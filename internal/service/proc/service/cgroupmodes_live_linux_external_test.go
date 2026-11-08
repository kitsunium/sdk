//go:build linux && !mips && !mipsle && !mips64 && !mips64le

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
	"github.com/kitsunium/sdk/internal/service/proc/service"
)

// The cgroup modes (S0g, kitsunium/sdk#734) in a real cgroup v2 group: each
// test makes its own group under a root delegated to it (liveUnitGroup), the
// unit's processes spawned into it, and skips where none is delegated. Only
// processes of that group — the test's own — are signalled.

// liveMemoryGroup makes the test's group with the memory controller, memory.max
// at limit and no swap, read back from the kernel — or skips: an OOM test
// must never run unbounded.
func liveMemoryGroup(t *testing.T, limit int64) coreproc.Group {
	t.Helper()
	root := liveCgroupRoot(t)
	name := "sdk-svc-oom-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	g, err := cgroup.Create(name, cgroup.WithRoot(root), cgroup.WithControllers(coreproc.ControllerMemory))
	if errs.HasCode(err, coreproc.CodeCgroupControllerAbsent) {
		t.Skipf("the memory controller is not offered under %s", root)
	}
	if errs.HasCode(err, coreproc.CodeCgroupWriteFailed) && os.Getenv(liveCgroupRootEnv) == "" {
		// A group holding this process takes no controller for its children:
		// its scope is this test's own, not a delegated root. A root the
		// operator named in KITSUNIUM_CGROUP_TEST_ROOT was promised usable, so
		// its refusal fails the test, below.
		t.Skipf("the memory controller cannot be enabled under %s: %v", root, err)
	}
	if err != nil {
		t.Fatalf("making the test's group: %v", err)
	}
	t.Cleanup(func() { removeLiveGroup(t, g) })
	dir := g.(coreproc.GroupTree).Path()
	if err := g.SetMemoryMax(limit); err != nil {
		t.Skipf("memory.max refused: %v", err)
	}
	if raw, rerr := os.ReadFile(filepath.Join(dir, "memory.max")); rerr != nil || strings.TrimSpace(string(raw)) != strconv.FormatInt(limit, 10) {
		t.Skipf("memory.max reads %q, %v: not bounded", raw, rerr)
	}
	if serr := g.(coreproc.GroupTuner).SetMemorySwapMax(0); serr != nil {
		if swaps, _ := os.ReadFile("/proc/swaps"); strings.Count(string(swaps), "\n") > 1 {
			t.Skipf("swap is on and memory.swap.max refused: %v", serr)
		}
	}
	return g
}

// removeLiveGroup kills what is left in g — the test's processes, nothing
// else can be there — waits for it to empty, and removes it with whatever was
// made beneath it.
func removeLiveGroup(t *testing.T, g coreproc.Group) {
	t.Helper()
	_ = g.Kill()
	lister, _ := g.(coreproc.GroupReader)
	deadline := time.Now().Add(5 * time.Second)
	for lister != nil && time.Now().Before(deadline) {
		if pids, lerr := lister.Members(); lerr != nil || len(pids) == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if derr := g.(coreproc.GroupTree).DeleteTree(); derr != nil {
		t.Errorf("removing the test's group: %v", derr)
	}
}

// liveGroupUnit asserts that g is what the cgroup modes need and returns it
// as a Config.Cgroup.
func liveGroupUnit(t *testing.T, g coreproc.Group) coresvc.CgroupKill {
	t.Helper()
	cg, ok := g.(coresvc.CgroupKill)
	if _, unit := g.(coresvc.CgroupUnit); !ok || !unit {
		t.Fatalf("the Linux group %T is no CgroupKill and CgroupUnit", g)
	}
	return cg
}

// TestLiveKillModes: each KillMode= in a real group, over a main process that
// forked a child. Both are spawned into the unit's group; process leaves the
// child, none both, control-group (the default here) and mixed none — the
// group empty once Stop returned.
func TestLiveKillModes(t *testing.T) {
	for _, tc := range []struct {
		name                string
		mode                coresvc.KillMode
		mainLeft, childLeft bool
	}{
		{"process", coresvc.KillModeProcess, false, true},
		{"none", coresvc.KillModeNone, true, true},
		{"default", coresvc.KillModeDefault, false, false},
		{"mixed", coresvc.KillModeMixed, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := liveUnitGroup(t)
			cg := liveGroupUnit(t, g)
			pidFile := filepath.Join(t.TempDir(), "child")
			u := realUnit(t, service.Config{
				KillMode: tc.mode, Cgroup: cg, TimeoutStop: 10 * time.Second,
				Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
				ExecStart: []coresvc.CommandValue{forkingChild(t, pidFile)},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := u.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			main := u.State().MainPID
			waitFile(t, pidFile)
			child := readPID(t, pidFile)
			for _, pid := range []int{main, child} {
				if in, err := cg.Contains(pid); err != nil || !in {
					t.Fatalf("Contains(%d) = %v, %v: not spawned into the unit's group", pid, in, err)
				}
			}
			if err := u.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
			members, _ := g.(coreproc.GroupReader).Members()
			if got := slices.Contains(members, main); got != tc.mainLeft {
				t.Errorf("main process left: %v (members %v), want %v", got, members, tc.mainLeft)
			}
			if got := slices.Contains(members, child); got != tc.childLeft {
				t.Errorf("forked child left: %v (members %v), want %v", got, members, tc.childLeft)
			}
		})
	}
}

// TestLiveExitTypeCgroup: in a real group, a main process that exits at once
// leaves the unit active on its child; the group's emptying, announced by
// cgroup.events, ends the unit — not before the child is gone.
func TestLiveExitTypeCgroup(t *testing.T) {
	g := liveUnitGroup(t)
	u := realUnit(t, service.Config{
		ExitType: coresvc.ExitTypeCgroup, Cgroup: liveGroupUnit(t, g),
		Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", "sleep 0.5 & exit 0")},
	})
	sub, cancel := u.Subscribe(64)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	upAlone := false
	for {
		select {
		case ev := <-sub:
			if ev.State == coresvc.StateActive && ev.MainPID == 0 && ev.MainExited {
				members, _ := g.(coreproc.GroupReader).Members()
				upAlone = len(members) > 0
			}
			if ev.State == coresvc.StateInactive {
				members, _ := g.(coreproc.GroupReader).Members()
				if !upAlone || len(members) != 0 {
					t.Fatalf("ended: up on the child alone %v, members left %v", upAlone, members)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("no end: %+v", u.State())
		}
	}
}

// TestLiveDelegate: Delegate= in a real group — the service makes a group of
// its own beneath the unit's and moves itself into it, the unit's Members
// still listing it (the subtree); the stop removes that group and keeps the
// unit's.
func TestLiveDelegate(t *testing.T) {
	g := liveUnitGroup(t)
	cg := liveGroupUnit(t, g)
	dir := g.(coreproc.GroupTree).Path()
	ready := filepath.Join(t.TempDir(), "ready")
	script := `mkdir "$GROUP_DIR/service" && echo $$ > "$GROUP_DIR/service/cgroup.procs" && echo $$ > '` + ready + `' && exec sleep 60`
	u := realUnit(t, service.Config{
		Delegate: true, Cgroup: cg,
		Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin", "GROUP_DIR=" + dir}},
		ExecStart: []coresvc.CommandValue{command(t, ":/bin/sh", "-c", script)},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := u.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFile(t, ready)
	main := readPID(t, ready)
	if raw, err := os.ReadFile(filepath.Join(dir, "service", "cgroup.procs")); err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(main) {
		t.Fatalf("the service's own group holds %q, %v; want %d", raw, err, main)
	}
	if members, err := g.(coreproc.GroupReader).Members(); err != nil || !slices.Contains(members, main) {
		t.Fatalf("Members = %v, %v: the service moved beneath is no longer listed", members, err)
	}
	if err := u.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "service")); !os.IsNotExist(err) {
		t.Fatalf("the service's group is still there after the stop: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the unit's group went with it: %v", err)
	}
}

// oomScript runs tail on /dev/zero — a line that never ends, held in memory
// until the OOM killer ends it —, bounded by RLIMIT_AS too, so a group the
// limit did not reach could never take the host's memory.
const oomScript string = "ulimit -v 262144; exec tail /dev/zero"

// TestLiveOOMPolicy: a real OOM kill in a group bounded by memory.max.
// OOMPolicy=stop (the default): the main process killed, the unit failed with
// oom-kill. continue: a worker killed, the main process and the unit running
// on. kill: memory.oom.group set, the whole group killed, the unit failed
// with oom-kill.
func TestLiveOOMPolicy(t *testing.T) {
	const limit = 32 << 20
	worker := "(" + oomScript + ") & exec sleep 60"
	for _, tc := range []struct {
		name   string
		policy coresvc.OOMPolicy
		script string
		fails  bool
	}{
		{"stop", coresvc.OOMPolicyDefault, oomScript, true},
		{"continue", coresvc.OOMPolicyContinue, worker, false},
		{"kill", coresvc.OOMPolicyKill, worker, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := liveMemoryGroup(t, limit)
			u := realUnit(t, service.Config{
				OOMPolicy: tc.policy, Cgroup: liveGroupUnit(t, g),
				Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
				ExecStart: []coresvc.CommandValue{command(t, "/bin/sh", "-c", tc.script)},
			})
			sub, cancel := u.Subscribe(64)
			defer cancel()
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			if err := u.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if tc.fails {
				ev := waitState(t, sub, coresvc.StateFailed)
				if ev.Result != coresvc.ResultOOMKill {
					t.Fatalf("result %s, want oom-kill", ev.Result)
				}
				return
			}
			deadline := time.Now().Add(20 * time.Second)
			for {
				stat, err := g.(coreproc.GroupReader).Stat()
				if err == nil && stat.MemoryEvents.OOMKill > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("no OOM kill in the group: %+v, %v", stat, err)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := u.ResetFailed(ctx); err != nil {
				t.Fatal(err)
			}
			if st := u.State(); st.State != coresvc.StateActive || st.MainPID == 0 {
				t.Fatalf("after a worker's OOM kill: %s main %d, want running on", st.State, st.MainPID)
			}
			if err := u.Stop(ctx); err != nil {
				t.Fatalf("Stop: %v", err)
			}
		})
	}
}

// TestLiveMembershipBoundsAdoption: an Attacher bounded by the unit's group
// adopts the daemon its PID file names when the bound is the path
// cgroup.Membership gives; the group's directory, GroupTree.Path, is no such
// path and every adoption is refused (ADOPTION_REFUSED, the start failing
// after TimeoutStartSec=) — the trap the conversion closes.
func TestLiveMembershipBoundsAdoption(t *testing.T) {
	g := liveUnitGroup(t)
	cg := liveGroupUnit(t, g)
	dir := g.(coreproc.GroupTree).Path()
	member, err := cgroup.Membership(dir)
	if err != nil || strings.HasPrefix(member, "/sys/") {
		t.Fatalf("Membership(%q) = %q, %v", dir, member, err)
	}
	for _, tc := range []struct {
		name  string
		bound string
		ok    bool
	}{
		{"Membership", member, true},
		{"the directory", dir, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "daemon.pid")
			u := realUnit(t, service.Config{
				Type: coresvc.TypeForking, PIDFile: pidFile, Cgroup: cg, TimeoutStart: time.Second,
				Attacher:  service.NewAttacher(coreproc.AttachValue{Cgroup: tc.bound}),
				Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
				ExecStart: []coresvc.CommandValue{daemonize(t, `echo $$ > "`+pidFile+`"; exec sleep 60`, pidFile)},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err := u.Start(ctx)
			if tc.ok {
				if err != nil || u.State().MainPID != readPID(t, pidFile) {
					t.Fatalf("bounded by %q: %v, main %d", tc.bound, err, u.State().MainPID)
				}
				return
			}
			if !errs.HasCode(err, coresvc.CodeStartFailed) || !strings.Contains(err.Error()+fieldOf(err, "pidfile"), "ADOPTION_REFUSED") {
				t.Fatalf("bounded by the directory %q: %v (result %q, pidfile %q, state %+v), want START_FAILED after ADOPTION_REFUSED", tc.bound, err, fieldOf(err, "result"), fieldOf(err, "pidfile"), u.State())
			}
		})
	}
}

// fieldOf is an error's field value, "" when it has none.
func fieldOf(err error, key string) string {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == key {
			return f.StringValue()
		}
	}
	return ""
}

// TestLiveStartsAgainAfterTheKill: a stop that ends with the final kill —
// the main process ignoring SIGTERM — leaves a group the unit starts in
// again: its next main process runs. The kill goes through the group's
// Signal: after a cgroup.kill, Linux 6.12 kills every child cloned into the
// group (CLONE_INTO_CGROUP) at its fork, and the unit could never start again.
func TestLiveStartsAgainAfterTheKill(t *testing.T) {
	g := liveUnitGroup(t)
	u := realUnit(t, service.Config{
		Cgroup: liveGroupUnit(t, g), TimeoutStop: 200 * time.Millisecond,
		Spec:      coreproc.Spec{Env: []string{"PATH=/usr/bin:/bin"}},
		ExecStart: []coresvc.CommandValue{command(t, "/bin/sh", "-c", "trap '' TERM; while :; do sleep 1; done")},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for round := range 2 {
		if err := u.Start(ctx); err != nil {
			t.Fatalf("Start %d: %v", round, err)
		}
		time.Sleep(300 * time.Millisecond)
		if st := u.State(); st.State != coresvc.StateActive || st.MainPID == 0 {
			t.Fatalf("round %d, 300 ms after Start: %s main %d (result %s), want running", round, st.State, st.MainPID, st.Result)
		}
		if err := u.Stop(ctx); err != nil {
			t.Fatalf("Stop %d: %v", round, err)
		}
		if st := u.State(); st.Result != coresvc.ResultTimeout {
			t.Fatalf("round %d: the stop ended with %s, want the final kill's timeout", round, st.Result)
		}
		if err := u.ResetFailed(ctx); err != nil {
			t.Fatal(err)
		}
	}
}
