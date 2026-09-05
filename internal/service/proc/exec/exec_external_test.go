//go:build unix

// Package exec_test — black-box acceptance tests for the spawn primitive: a
// normal-exit Wait, a group-kill-with-survivor check, and the SIGTERM→SIGKILL
// escalation, plus the typed-error contracts. Unix-only behaviour is gated on a
// /bin/sh probe so the suite degrades cleanly where it cannot run.
package exec_test

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcexec "github.com/kitsunium/sdk/internal/service/proc/exec"
)

// shPath is the shell used by the spawn tests; an absent /bin/sh skips them.
const shPath = "/bin/sh"

// requireShell skips the calling test when the host is non-Unix or lacks
// /bin/sh, while still leaving the typed-error tests to assert the contract.
func requireShell(t *testing.T) {
	t.Helper()
	//: a missing shell means the fixture commands cannot run.
	if _, err := os.Stat(shPath); err != nil {
		//: skip rather than fail when the container has no /bin/sh.
		t.Skipf("%s not present: %v", shPath, err)
	}
}

// TestWaitNormalExit asserts Wait reports the exact status of a clean exit.
func TestWaitNormalExit(t *testing.T) {
	requireShell(t)

	type exitCase struct {
		name string
		code int
	}
	//: cover a zero exit and a non-zero status; both must round-trip.
	cases := []exitCase{
		{name: "zero", code: 0},
		{name: "nonzero", code: 7},
	}

	runCase := func(t *testing.T, tc exitCase) {
		t.Helper()
		spec := coreproc.Spec{
			Path: shPath,
			Args: []string{"sh", "-c", "exit " + strconv.Itoa(tc.code)},
		}
		p, err := svcexec.Start(t.Context(), spec)
		//: a clean spawn of /bin/sh must not error.
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		//: a normal exit is not a wait4 fault.
		if wErr != nil {
			t.Fatalf("Wait: %v", wErr)
		}
		//: the reported code must equal the shell's exit status.
		if exit.Code != tc.code {
			t.Fatalf("Code = %d, want %d", exit.Code, tc.code)
		}
		//: a normal exit is never marked signalled.
		if exit.Signaled {
			t.Fatalf("Signaled = true, want false for a normal exit")
		}
		//: a zero exit must report Success.
		if tc.code == 0 && !exit.Success() {
			t.Fatalf("Success() = false, want true for exit 0")
		}
	}

	for _, tc := range cases {
		//: subtest per exit code isolates a regression to one status.
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestWaitSignaledExit asserts a signalled death is reported as such with the
// terminating signal and a -1 code.
func TestWaitSignaledExit(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "sleep 30"},
		Setpgid: true,
	}
	p, err := svcexec.Start(t.Context(), spec)
	//: a clean spawn is the precondition for the signal test.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: deliver an immediate, unignorable kill to the leader.
	if sErr := p.Signal(coreproc.Signal(syscall.SIGKILL)); sErr != nil {
		t.Fatalf("Signal: %v", sErr)
	}
	exit, wErr := p.Wait()
	//: reaping a signalled child is not itself a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: a signalled death must be flagged.
	if !exit.Signaled {
		t.Fatalf("Signaled = false, want true after SIGKILL")
	}
	//: the terminating signal must be the one delivered.
	if exit.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("Signal = %v, want SIGKILL", exit.Signal)
	}
	//: a signalled exit reports -1, never a normal status code.
	if exit.Code != -1 {
		t.Fatalf("Code = %d, want -1 for a signalled exit", exit.Code)
	}
}

// TestStopGroupNoSurvivor spawns a leader that forks a grandchild and asserts
// Stop leaves no survivor in the process group — the acceptance (a) contract.
func TestStopGroupNoSurvivor(t *testing.T) {
	t.Parallel()
	requireShell(t)

	//: the leader backgrounds a long sleep then waits — the grandchild is the
	//: survivor risk a leader-only kill would leave behind.
	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "sleep 30 & echo $! ; wait"},
		Setpgid: true,
	}
	p, err := svcexec.Start(t.Context(), spec)
	//: a clean spawn is the precondition for the group-kill test.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pgid := p.PID()

	//: give the shell a moment to fork the backgrounded sleep into the group.
	time.Sleep(150 * time.Millisecond)

	//: Stop must terminate the whole group, grandchild included.
	if sErr := p.Stop(t.Context(), 10*time.Second, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Stop: %v", sErr)
	}
	//: drive the reap of the leader so the group is fully settled.
	if _, wErr := p.Wait(); wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}

	//: after a short settle, the process group must be entirely gone — a
	//: kill(-pgid, 0) probe returns ESRCH when no member survives.
	deadline := time.Now().Add(10 * time.Second)
	//: poll until the group reports gone or the deadline fails the test.
	for {
		err := syscall.Kill(-pgid, 0)
		//: ESRCH means no surviving member — the acceptance (a) success.
		if err == syscall.ESRCH {
			//: group fully reaped; nothing leaked.
			return
		}
		//: past the deadline with a survivor is a hard failure.
		if time.Now().After(deadline) {
			t.Fatalf("process group %d still has a survivor (kill probe err=%v)", pgid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestStopEscalatesToKill asserts Stop escalates SIGTERM→SIGKILL after grace
// for a child that ignores SIGTERM — the acceptance (b) contract.
func TestStopEscalatesToKill(t *testing.T) {
	t.Parallel()
	requireShell(t)

	//: trap-ignore SIGTERM, then sleep; only SIGKILL can take this child down.
	spec := coreproc.Spec{
		Path:    shPath,
		Args:    []string{"sh", "-c", "trap '' TERM; sleep 30"},
		Setpgid: true,
	}
	p, err := svcexec.Start(t.Context(), spec)
	//: a clean spawn is the precondition for the escalation test.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: let the shell install its TERM trap before we signal it.
	time.Sleep(150 * time.Millisecond)

	grace := 300 * time.Millisecond
	start := time.Now()
	//: Stop must return only after escalating to SIGKILL past the grace window.
	if sErr := p.Stop(t.Context(), grace, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Stop: %v", sErr)
	}
	elapsed := time.Since(start)
	//: escalation cannot have happened before the grace window elapsed.
	if elapsed < grace {
		t.Fatalf("Stop returned in %v, before the %v grace — no escalation", elapsed, grace)
	}

	exit, wErr := p.Wait()
	//: reaping the killed child is not a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: the child must have died by signal, not a normal exit.
	if !exit.Signaled {
		t.Fatalf("Signaled = false, want true after SIGKILL escalation")
	}
	//: the terminating signal must be SIGKILL — SIGTERM was ignored.
	if exit.Signal != coreproc.Signal(syscall.SIGKILL) {
		t.Fatalf("Signal = %v, want SIGKILL after escalation", exit.Signal)
	}
}

// TestStartInvalidSpec asserts an empty Path is rejected with InvalidSpec on
// every platform (no shell needed — the contract holds even where spawn cannot).
func TestStartInvalidSpec(t *testing.T) {
	t.Parallel()

	_, err := svcexec.Start(t.Context(), coreproc.Spec{})
	//: an empty Path must surface the central INVALID_SPEC code.
	if !errs.HasCode(err, coreproc.CodeInvalidSpec) {
		t.Fatalf("Start(empty) err = %v, want CodeInvalidSpec", err)
	}
}

// TestStartUnknownUser asserts a non-existent user surfaces UnknownUser.
func TestStartUnknownUser(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path: shPath,
		Args: []string{"sh", "-c", "true"},
		User: "this-user-does-not-exist-kitsunium",
	}
	_, err := svcexec.Start(t.Context(), spec)
	//: an unresolvable user must surface the central UNKNOWN_USER code.
	if !errs.HasCode(err, coreproc.CodeUnknownUser) {
		t.Fatalf("Start(bad user) err = %v, want CodeUnknownUser", err)
	}
}

// TestStartUnknownGroup asserts a non-existent group surfaces UnknownGroup.
func TestStartUnknownGroup(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path:  shPath,
		Args:  []string{"sh", "-c", "true"},
		Group: "this-group-does-not-exist-kitsunium",
	}
	_, err := svcexec.Start(t.Context(), spec)
	//: an unresolvable group must surface the central UNKNOWN_GROUP code.
	if !errs.HasCode(err, coreproc.CodeUnknownGroup) {
		t.Fatalf("Start(bad group) err = %v, want CodeUnknownGroup", err)
	}
}

// TestStartUnknownResource asserts a resource with no RLIMIT_* mapping surfaces
// UnknownResource before any spawn happens.
func TestStartUnknownResource(t *testing.T) {
	t.Parallel()

	spec := coreproc.Spec{
		Path: shPath,
		//: ResourceNProc has no stdlib RLIMIT_NPROC mapping — unmappable here.
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNProc: {Soft: 64, Hard: 64},
		},
	}
	_, err := svcexec.Start(t.Context(), spec)
	//: an unmapped resource must surface the central UNKNOWN_RESOURCE code.
	if !errs.HasCode(err, coreproc.CodeUnknownResource) {
		t.Fatalf("Start(bad resource) err = %v, want CodeUnknownResource", err)
	}
}

// TestStartLimitsHonoured asserts that a mappable Rlimit and a Umask are actually
// applied to the child — not rejected — via the re-exec trampoline. Each case
// spawns a shell that prints the effective limit and the captured stdout (#73
// StdioCapture) is compared against the requested value. The trampoline re-execs
// this very test binary, whose linked exec package applies the limit before
// exec'ing /bin/sh, so these cases exercise the full pre-exec path.
func TestStartLimitsHonoured(t *testing.T) {
	t.Parallel()
	requireShell(t)

	type limitCase struct {
		name   string
		probe  string
		mutate func(spec *coreproc.Spec)
		want   int64
		base   int
	}
	//: cover the soft NOFILE limit, a zeroed core limit, and the umask — each
	//: applied in the child by the trampoline and read back from the shell.
	cases := []limitCase{
		{
			name:  "nofile_soft",
			probe: "ulimit -n",
			mutate: func(spec *coreproc.Spec) {
				//: lower NOFILE well below any default so the readback is unambiguous.
				spec.Rlimits = map[coreproc.Resource]coreproc.LimitValue{
					coreproc.ResourceNoFile: {Soft: 48, Hard: 48},
				}
			},
			want: 48,
			base: 10,
		},
		{
			name:  "core_zero",
			probe: "ulimit -c",
			mutate: func(spec *coreproc.Spec) {
				//: a zero core limit disables core dumps for the child.
				spec.Rlimits = map[coreproc.Resource]coreproc.LimitValue{
					coreproc.ResourceCore: {Soft: 0, Hard: 0},
				}
			},
			want: 0,
			base: 10,
		},
		{
			name:  "umask",
			probe: "umask",
			mutate: func(spec *coreproc.Spec) {
				//: the shell prints the umask in octal; 0o077 reads back as 63.
				//: new(expr) is the Go 1.26 pointer-to-value form (ktn-linter requires
				//: it over a named local; CI compiles it).
				spec.Umask = new(0o077)
			},
			want: 0o077,
			base: 8,
		},
	}

	runCase := func(t *testing.T, tc limitCase) {
		t.Helper()
		var out strings.Builder
		spec := coreproc.Spec{
			Path:   shPath,
			Args:   []string{"sh", "-c", tc.probe},
			Stdio:  coreproc.StdioCapture,
			Stdout: &out,
		}
		tc.mutate(&spec)
		p, err := svcexec.Start(t.Context(), spec)
		//: a clean spawn through the trampoline is the precondition for the readback.
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		exit, wErr := p.Wait()
		//: a normal exit is not a wait4 fault.
		if wErr != nil {
			t.Fatalf("Wait: %v", wErr)
		}
		//: the probe shell must itself exit cleanly for its output to be trusted.
		if !exit.Success() {
			t.Fatalf("%s: probe shell exit %d", tc.name, exit.Code)
		}
		got := strings.TrimSpace(out.String())
		gotN, pErr := strconv.ParseInt(got, tc.base, 64)
		//: the probe must print a parseable number in the documented base.
		if pErr != nil {
			t.Fatalf("%s: unparseable probe output %q: %v", tc.name, got, pErr)
		}
		//: the effective limit in the child must equal the requested value.
		if gotN != tc.want {
			t.Fatalf("%s: %q = %q (%d), want %d", tc.name, tc.probe, got, gotN, tc.want)
		}
	}

	//: each limit case spawns its own probe shell through the trampoline.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runCase(t, tc) })
	}
}

// TestStartTrampolineApplyFailureTyped asserts that when the trampoline cannot
// apply a requested limit — here an invalid Soft>Hard pair, which setrlimit
// rejects with EINVAL regardless of privilege — Start surfaces the typed
// RlimitFailed through the handshake pipe, not a silent non-zero child exit the
// caller could not distinguish from a legitimate target exit.
//
// Linux, Darwin, NetBSD and OpenBSD reject Soft>Hard with EINVAL, so the error
// path is exercised there. FreeBSD/DragonFly's setrlimit instead CLAMPS Soft to
// Hard rather than failing, so this particular input cannot reproduce the apply
// failure on them — skip there. The trampoline's failure-propagation mechanic is
// platform-neutral code and stays covered on the four kernels that reject it.
func TestStartTrampolineApplyFailureTyped(t *testing.T) {
	t.Parallel()
	//: FreeBSD/DragonFly clamp Soft>Hard instead of EINVAL, so the failure this
	//: test injects is not reproducible there; the mechanic is covered elsewhere.
	if runtime.GOOS == "freebsd" || runtime.GOOS == "dragonfly" {
		t.Skip("FreeBSD/DragonFly setrlimit clamps Soft>Hard rather than rejecting it")
	}

	spec := coreproc.Spec{
		Path: shPath,
		Args: []string{"sh", "-c", "true"},
		//: Soft above Hard is an invalid pair setrlimit refuses with EINVAL; the
		//: trampoline fails before exec, so /bin/sh need not even be present.
		Rlimits: map[coreproc.Resource]coreproc.LimitValue{
			coreproc.ResourceNoFile: {Soft: 100, Hard: 50},
		},
	}
	_, err := svcexec.Start(t.Context(), spec)
	//: a trampoline apply failure must surface the typed RLIMIT_FAILED.
	if !errs.HasCode(err, coreproc.CodeRlimitFailed) {
		t.Fatalf("Start(invalid rlimit pair) err = %v, want CodeRlimitFailed", err)
	}
}

// TestStartTrampolineExecFailureTyped asserts that when the trampoline applies
// the limits but cannot execve the target (a non-existent path), Start surfaces
// the typed SpawnFailed through the handshake — matching the direct-spawn path's
// contract instead of a bare 127 child exit.
func TestStartTrampolineExecFailureTyped(t *testing.T) {
	t.Parallel()

	//: a Umask routes the spawn through the trampoline; the bad Path makes the
	//: trampoline's execve fail after the umask is applied.
	spec := coreproc.Spec{
		Path:  "/nonexistent/kitsunium-proc-exec-test",
		Umask: new(0o022),
	}
	_, err := svcexec.Start(t.Context(), spec)
	//: a trampoline exec failure must surface the typed SPAWN_FAILED.
	if !errs.HasCode(err, coreproc.CodeSpawnFailed) {
		t.Fatalf("Start(trampoline bad path) err = %v, want CodeSpawnFailed", err)
	}
}

// TestStartCgroupPathUnavailableTyped asserts the pre-spawn CgroupPath check
// (issue #91): a path that is not a usable cgroup v2 directory surfaces a typed
// error BEFORE any child is spawned — there is no unconfined window — and the
// error is platform-correct: CGROUP_UNAVAILABLE on Linux, UNSUPPORTED_PLATFORM
// on a non-Linux Unix host (cgroup v2 has no equivalent there).
func TestStartCgroupPathUnavailableTyped(t *testing.T) {
	t.Parallel()

	spec := coreproc.Spec{
		Path: shPath,
		Args: []string{"sh", "-c", "true"},
		//: a path that is not a cgroup v2 directory; validation fails pre-spawn so
		//: /bin/sh need not even be present.
		CgroupPath: "/nonexistent/kitsunium-proc-cgroup-test",
	}
	_, err := svcexec.Start(t.Context(), spec)
	//: Linux maps a missing / non-cgroup / non-delegated path to CGROUP_UNAVAILABLE.
	if runtime.GOOS == "linux" {
		//: the typed CGROUP_UNAVAILABLE proves the pre-spawn guard fired.
		if !errs.HasCode(err, coreproc.CodeCgroupUnavailable) {
			t.Fatalf("Start(bad CgroupPath) on linux err = %v, want CodeCgroupUnavailable", err)
		}
		return
	}
	//: every other Unix has no cgroup v2 → the uniform UNSUPPORTED_PLATFORM contract.
	if !errs.HasCode(err, coreproc.CodeUnsupportedPlatform) {
		t.Fatalf("Start(CgroupPath) on %s err = %v, want CodeUnsupportedPlatform", runtime.GOOS, err)
	}
}

// TestStartExtraFilesSurviveTrampoline proves the fd-ordering fix that #91's
// trampoline-forcing CgroupPath made load-bearing: when a spawn routes through
// the re-exec trampoline AND carries ExtraFiles, the extras must still land at
// fd 3.. (the socket-activation contract), with the handshake pipe appended
// AFTER them. The child reads its fd 3 directly; if the handshake had taken fd 3
// (the old bug) the read would see the parent-closed pipe (EOF) instead.
func TestStartExtraFilesSurviveTrampoline(t *testing.T) {
	t.Parallel()
	//: the child is a real shell; skip cleanly where the host has none.
	if _, statErr := os.Stat(shPath); statErr != nil {
		//: no /bin/sh — the fd-inheritance behaviour cannot be exercised here.
		t.Skip("no /bin/sh to exercise fd inheritance")
	}
	//: an ExtraFile carrying a known payload the child must read back from fd 3.
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	const want = "fd3-payload"
	//: write the small payload synchronously (it fits the pipe buffer, no block)
	//: then close to give the child's `cat <&3` an EOF — no goroutine needed.
	if _, wErr := w.WriteString(want); wErr != nil {
		t.Fatalf("write payload: %v", wErr)
	}
	if wcErr := w.Close(); wcErr != nil {
		t.Fatalf("close payload writer: %v", wcErr)
	}
	var out strings.Builder
	spec := coreproc.Spec{
		Path: shPath,
		//: read everything from fd 3 (the first ExtraFile) and echo it to stdout.
		Args: []string{"sh", "-c", "cat <&3"},
		//: a non-nil Umask forces the spawn through the trampoline + handshake pipe.
		Umask:      new(0o022),
		ExtraFiles: []*os.File{r},
		Stdio:      coreproc.StdioCapture,
		Stdout:     &out,
	}
	proc, err := svcexec.Start(t.Context(), spec)
	//: close the parent's copy of the read end; the child inherited its own.
	if rcErr := r.Close(); rcErr != nil {
		t.Fatalf("close ExtraFile read end: %v", rcErr)
	}
	if err != nil {
		t.Fatalf("Start(trampoline + ExtraFiles): %v", err)
	}
	//: Wait joins the capture copier, so out holds the child's full stdout.
	if _, werr := proc.Wait(); werr != nil {
		t.Fatalf("Wait: %v", werr)
	}
	//: the child must have read the payload from fd 3 — proof the ExtraFile kept
	//: fd 3 through the trampoline rather than being shifted to 4 by the handshake.
	if got := strings.TrimSpace(out.String()); got != want {
		t.Fatalf("child read %q from fd 3, want %q — ExtraFile was shifted by the trampoline handshake", got, want)
	}
}

// TestStartContextCancelled asserts an already-cancelled context aborts the
// spawn before any OS work.
func TestStartContextCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svcexec.Start(ctx, coreproc.Spec{Path: shPath})
	//: a cancelled context must surface context.Canceled, not a spawn.
	if err != context.Canceled {
		t.Fatalf("Start(cancelled) err = %v, want context.Canceled", err)
	}
}

// TestStartEmptyEnvNoLeak asserts a nil Spec.Env spawns an EMPTY environment:
// the supervisor's variables (a canary here) must never leak into the child.
// Regression for the nil-Env inheritance bug.
func TestStartEmptyEnvNoLeak(t *testing.T) {
	requireShell(t)
	//: a canary in the supervisor env must not reach a nil-Env child.
	t.Setenv("PROC_ENV_LEAK_CANARY", "leaked")

	spec := coreproc.Spec{
		Path: shPath,
		//: the shell exits 0 only when the canary is absent (empty environment).
		Args: []string{"sh", "-c", "[ -z \"$PROC_ENV_LEAK_CANARY\" ]"},
	}
	p, err := svcexec.Start(t.Context(), spec)
	//: a clean spawn is the precondition for the leak check.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	exit, wErr := p.Wait()
	//: a normal exit is not a wait4 fault.
	if wErr != nil {
		t.Fatalf("Wait: %v", wErr)
	}
	//: a non-zero status means the canary leaked into the child environment.
	if exit.Code != 0 {
		t.Fatalf("nil Env leaked the supervisor environment (child exit %d)", exit.Code)
	}
}

// TestStopWithoutSetpgid asserts Stop terminates a child that does NOT lead its
// own group: the group operation degrades to the leader instead of no-oping on a
// missing group (which would leak the child). Regression for the pgid==pid bug.
func TestStopWithoutSetpgid(t *testing.T) {
	t.Parallel()
	requireShell(t)

	spec := coreproc.Spec{
		Path: shPath,
		Args: []string{"sh", "-c", "sleep 30"},
		//: deliberately NOT a group leader — exercises the degrade-to-leader path.
		Setpgid: false,
	}
	p, err := svcexec.Start(t.Context(), spec)
	//: a clean spawn is the precondition for the stop check.
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	//: Stop must actually terminate the leader, not silently no-op on -pid ESRCH.
	if sErr := p.Stop(t.Context(), time.Second, coreproc.Signal(syscall.SIGTERM)); sErr != nil {
		t.Fatalf("Stop: %v", sErr)
	}
	//: after Stop the leader must be gone — kill(pid, 0) reports ESRCH.
	if err := syscall.Kill(p.PID(), 0); err == nil {
		t.Fatalf("child %d survived Stop without Setpgid", p.PID())
	}
}
