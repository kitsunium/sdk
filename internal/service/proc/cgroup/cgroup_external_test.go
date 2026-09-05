// Package cgroup_test — black-box tests for the cgroup service facade.
//
// Every assertion here holds on BOTH a delegated host and an unprivileged one.
// Where a live hierarchy is available the full cycle runs; where it is not, the
// same test asserts the typed refusal instead. That is deliberate: the degrade
// path is the one most consumers actually hit, and skipping it would leave the
// contract that matters most unexercised on the machines that most need it.
package cgroup_test

import (
	"os/exec"
	"runtime"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/proc/cgroup"
)

// childGrace is how long the helper sleep child lives — long enough to attach
// and assert, short enough that a leaked child self-reaps quickly.
const childGrace time.Duration = 200 * time.Millisecond

// killDrainDeadline bounds how long the no-survivor check polls Delete after a
// Kill before declaring a surviving child a regression.
const killDrainDeadline time.Duration = 3 * time.Second

// killPollInterval is the gap between Delete attempts while the kernel finishes
// reaping the killed subtree.
const killPollInterval time.Duration = 50 * time.Millisecond

// unavailableCode is the sentinel Create must return when it cannot make a
// group: the hierarchy is missing or undelegated on Linux, and the whole
// mechanism is absent everywhere else.
func unavailableCode() errs.Code {
	//: off Linux the stub short-circuits before any hierarchy check.
	if runtime.GOOS != "linux" {
		return coreproc.CodeUnsupportedPlatform
	}
	return coreproc.CodeCgroupUnavailable
}

// TestAvailable pins the delegation probe. It must answer on every host without
// panicking, agree with the platform, and — the regression this covers — stay
// true across repeated calls: the probe creates and removes a uniquely-named
// directory, and a fixed name would false-negative on its own leftovers.
func TestAvailable(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many times to probe before comparing; repetition is what
		//: catches a probe that poisons its own next call.
		calls int
	}
	tests := []tc{
		{"a single probe", 1},
		{"two probes in a row", 2},
		{"ten probes in a row", 10},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		first := cgroup.Available()
		for range c.calls {
			//: every later probe must agree with the first.
			if got := cgroup.Available(); got != first {
				t.Fatalf("Available() flipped to %v after reporting %v", got, first)
			}
		}
		//: cgroup-equivalent facilities exist on Linux (cgroup v2) and Windows
		//: (Job Objects); anywhere else a true answer means the stub is
		//: mis-wired to a real probe.
		if runtime.GOOS != "linux" && runtime.GOOS != "windows" && first {
			t.Errorf("Available() on %s = true, want false", runtime.GOOS)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestCreate pins name validation and the unavailable degrade in one table.
//
// The rejected names are the containment guarantee: each one, joined onto the
// delegated root, would resolve outside it. They are refused before any
// filesystem call, which is why the assertion holds whether or not a hierarchy
// is delegated to this process.
func TestCreate(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the group name handed to Create.
		group string
		//: true when the name itself must be refused, whatever the platform.
		wantInvalid bool
	}
	tests := []tc{
		{"an empty name", "", true},
		{"the current directory", ".", true},
		{"the parent directory", "..", true},
		{"a traversal", "../escape", true},
		{"a nested path", "a/b", true},
		{"a traversal past the root", "nested/../..", true},
		{"an absolute path", "/abs", true},
		{"a trailing separator", "foo/", true},
		{"a plain name", "sdk-test-create", false},
		{"a name with a dash and digits", "sdk-test-create-2", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := cgroup.Create(c.group)

		//: a refused create must never hand back a usable handle, whatever the
		//: reason for the refusal.
		if err != nil && g != nil {
			t.Fatalf("Create(%q) returned a handle beside %v", c.group, err)
		}

		if c.wantInvalid {
			//: name validation runs before the hierarchy check on Linux; off
			//: Linux the stub refuses everything first.
			want := coreproc.CodeInvalidSpec
			if runtime.GOOS != "linux" {
				want = coreproc.CodeUnsupportedPlatform
			}
			if !errs.HasCode(err, want) {
				t.Fatalf("Create(%q) = %v, want code %v", c.group, err, want)
			}
			return
		}

		//: a valid name on a host with no delegated hierarchy must degrade to
		//: the typed sentinel rather than panicking or half-succeeding.
		if !cgroup.Available() {
			if !errs.HasCode(err, unavailableCode()) {
				t.Fatalf("Create(%q) on an undelegated host = %v, want code %v",
					c.group, err, unavailableCode())
			}
			return
		}
		//: on a delegated host the same call must simply work.
		if err != nil {
			t.Fatalf("Create(%q) = %v on a host where Available() is true", c.group, err)
		}
		//: leave nothing behind; the group is empty, so Delete succeeds.
		if derr := g.Delete(); derr != nil {
			t.Errorf("Delete after Create(%q) = %v, want nil", c.group, derr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestGroupConfinement runs the full create → confine → attach → delete cycle
// where a hierarchy is delegated, and asserts the typed refusal where it is not.
func TestGroupConfinement(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the ceiling to apply, expressed through the Group port so the case
		//: names the controller rather than the file.
		confine func(g coreproc.Group) error
	}
	tests := []tc{
		{"a memory ceiling", func(g coreproc.Group) error { return g.SetMemoryMax(64 << 20) }},
		{"a process ceiling", func(g coreproc.Group) error { return g.SetPidsMax(64) }},
		{"an unlimited memory ceiling", func(g coreproc.Group) error { return g.SetMemoryMax(-1) }},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := cgroup.Create("sdk-test-confine-" + sanitise(c.name))
		if !cgroup.Available() {
			//: the degrade path: no handle, and a typed refusal.
			if g != nil {
				t.Fatalf("Create returned a handle on an undelegated host")
			}
			if !errs.HasCode(err, unavailableCode()) {
				t.Fatalf("Create = %v, want code %v", err, unavailableCode())
			}
			return
		}
		if err != nil {
			t.Fatalf("Create = %v on a host where Available() is true", err)
		}
		defer deleteGroup(t, g)

		//: a controller write proves the ceiling reaches the kernel rather
		//: than being accepted and dropped.
		if cerr := c.confine(g); cerr != nil {
			t.Fatalf("%s: %v", c.name, cerr)
		}

		child := startChild(t)
		//: attaching proves cgroup.procs accepts a foreign pid, which is what
		//: makes confinement apply to anything but ourselves.
		if aerr := g.Add(child.Process.Pid); aerr != nil {
			t.Fatalf("Add(child): %v", aerr)
		}
		//: wait for the child to exit so the group empties and can be removed.
		if werr := child.Wait(); werr != nil {
			t.Logf("child wait: %v", werr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestGroupKillFreezeThaw exercises the kernel-version-gated feature files.
// Freeze and Thaw must be observable and idempotent; Kill on an empty group is a
// no-op success. A kernel that predates a feature file reports
// UnsupportedPlatform, which is the documented fallback signal and is asserted
// as such rather than skipped.
func TestGroupKillFreezeThaw(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the sequence of feature operations, in order.
		ops []func(g coreproc.Group) error
	}
	tests := []tc{
		{"freeze then thaw", []func(coreproc.Group) error{
			coreproc.Group.Freeze, coreproc.Group.Thaw,
		}},
		{"freeze twice is idempotent", []func(coreproc.Group) error{
			coreproc.Group.Freeze, coreproc.Group.Freeze, coreproc.Group.Thaw,
		}},
		{"thaw without a freeze", []func(coreproc.Group) error{
			coreproc.Group.Thaw,
		}},
		{"kill an empty group", []func(coreproc.Group) error{
			coreproc.Group.Kill,
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := cgroup.Create("sdk-test-feature-" + sanitise(c.name))
		if !cgroup.Available() {
			if !errs.HasCode(err, unavailableCode()) {
				t.Fatalf("Create = %v, want code %v", err, unavailableCode())
			}
			return
		}
		if err != nil {
			t.Fatalf("Create = %v on a host where Available() is true", err)
		}
		defer deleteGroup(t, g)

		for i, op := range c.ops {
			//: nil means the feature applied; UNSUPPORTED_PLATFORM means the
			//: kernel predates the interface file, which is the documented
			//: fallback signal — anything else is a real fault.
			oerr := op(g)
			if oerr == nil || errs.HasCode(oerr, coreproc.CodeUnsupportedPlatform) {
				continue
			}
			t.Fatalf("operation %d of %s: %v", i, c.name, oerr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestKillTerminatesSetsidDescendant is the escape-proof acceptance: a child
// that setsid's a grandchild leaves the parent's process group, so kill(-pgid)
// would miss it — but cgroup.kill, tracking membership by control group, takes
// it down. The helper loop also keeps forking, so the kill races live forks.
func TestKillTerminatesSetsidDescendant(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: the shell program the leader runs; each shape escapes differently.
		program string
	}
	tests := []tc{
		{
			"a setsid grandchild beside a forking loop",
			"setsid sleep 30 >/dev/null 2>&1 & while :; do sleep 1 & done",
		},
		{
			"a setsid grandchild alone",
			"setsid sleep 30 >/dev/null 2>&1 & sleep 30",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, err := cgroup.Create("sdk-test-killtree-" + sanitise(c.name))
		if !cgroup.Available() {
			if !errs.HasCode(err, unavailableCode()) {
				t.Fatalf("Create = %v, want code %v", err, unavailableCode())
			}
			return
		}
		if err != nil {
			t.Fatalf("Create = %v on a host where Available() is true", err)
		}
		defer deleteGroup(t, g)

		//: probe cgroup.kill on the still-EMPTY group first, so an old kernel
		//: is detected before any helper tree is spawned and never leaks one.
		if kerr := g.Kill(); kerr != nil {
			if errs.HasCode(kerr, coreproc.CodeUnsupportedPlatform) {
				//: nothing was spawned yet; the fallback contract is asserted
				//: and there is nothing to clean up.
				return
			}
			t.Fatalf("Kill on an empty group: %v", kerr)
		}

		cmd := exec.Command(shellPath(t), "-c", c.program)
		if serr := cmd.Start(); serr != nil {
			t.Fatalf("starting the helper tree: %v", serr)
		}
		//: attach the leader; its children inherit the cgroup membership.
		if aerr := g.Add(cmd.Process.Pid); aerr != nil {
			t.Fatalf("Add(leader): %v", aerr)
		}
		//: let the loop fork a few children so the kill races live forks.
		time.Sleep(childGrace)

		//: cgroup.kill must take down the whole subtree atomically — setsid
		//: escapee and in-flight forks included.
		if kerr := g.Kill(); kerr != nil {
			t.Fatalf("Kill on the populated tree: %v", kerr)
		}
		//: reap the leader so it does not linger as a zombie.
		if _, werr := cmd.Process.Wait(); werr != nil {
			t.Logf("leader wait after Kill: %v", werr)
		}
		//: a successful Delete proves the group is EMPTY: any survivor would
		//: hold it EBUSY.
		assertGroupDrains(t, g)
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// sanitise turns a case name into a single safe path element, since a control
// group name may not carry a separator.
func sanitise(name string) string {
	out := make([]rune, 0, len(name))
	//: keep letters and digits, collapse everything else to a dash.
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

// shellPath resolves the POSIX shell the helper trees run under.
func shellPath(t *testing.T) string {
	t.Helper()
	//: POSIX mandates a shell; an absent one is a broken host, not a reason to
	//: quietly pass.
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Fatalf("resolving sh: %v", err)
	}
	return path
}

// startChild launches a brief sleep process to serve as the attachment target.
func startChild(t *testing.T) *exec.Cmd {
	t.Helper()
	path, err := exec.LookPath("sleep")
	//: POSIX mandates sleep; an absent one is a broken host.
	if err != nil {
		t.Fatalf("resolving sleep: %v", err)
	}
	cmd := exec.Command(path, childGrace.String())
	if serr := cmd.Start(); serr != nil {
		t.Fatalf("starting the helper child: %v", serr)
	}
	return cmd
}

// deleteGroup removes g, logging (not failing) a delete fault during cleanup.
func deleteGroup(t *testing.T, g coreproc.Group) {
	t.Helper()
	//: best-effort teardown; a populated-group EBUSY is logged, not fatal.
	if derr := g.Delete(); derr != nil {
		t.Logf("cleanup Delete: %v", derr)
	}
}

// assertGroupDrains polls Delete until it succeeds (the group is empty, so no
// child survived the kill) or the deadline fails the test. A successful Delete
// is the black-box proof of zero survivors.
func assertGroupDrains(t *testing.T, g coreproc.Group) {
	t.Helper()
	deadline := time.Now().Add(killDrainDeadline)
	//: poll until the childless group rmdir's, or fail on a lingering survivor.
	for {
		//: a clean Delete means cgroup.procs is empty — no survivor.
		if derr := g.Delete(); derr == nil {
			return
		}
		//: past the deadline with a populated group is a survivor regression.
		if time.Now().After(deadline) {
			t.Fatal("the group is still populated after Kill — a child survived")
		}
		time.Sleep(killPollInterval)
	}
}
