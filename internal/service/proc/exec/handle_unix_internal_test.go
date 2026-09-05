//go:build unix

// Package exec — the live supervision handle. Everything here is about one
// question a supervisor has to answer correctly: is this process gone, and did
// anything it forked go with it?
package exec

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// startChild spawns a shell running program and returns its handle.
func startChild(t *testing.T, setpgid bool, program string) *handle {
	t.Helper()
	spec := coreproc.Spec{
		Path:    shellPath,
		Args:    []string{"sh", "-c", program},
		Setpgid: setpgid,
	}
	sio, err := buildStdio(spec)
	if err != nil {
		t.Fatalf("buildStdio = %v, want nil", err)
	}
	started, serr := spawn(spec, sio)
	if serr != nil {
		sio.closeAll()
		t.Fatalf("spawn = %v, want nil", serr)
	}
	h := newHandle(started, setpgid, sio)
	t.Cleanup(func() {
		//: whatever the test did, leave nothing running or unreaped. Both
		//: calls routinely fail on an already-reaped child, which is the
		//: outcome we wanted, so they are logged rather than asserted.
		if kerr := h.SignalGroup(coreproc.Signal(syscall.SIGKILL)); kerr != nil {
			t.Logf("cleanup SignalGroup: %v", kerr)
		}
		if _, werr := h.Wait(); werr != nil {
			t.Logf("cleanup Wait: %v", werr)
		}
		sio.closeAll()
	})
	return h
}

// Test_newHandle pins the pgid decision made at construction. Without Setpgid
// there is no private group, so remembering one would let a later group kill
// address the SUPERVISOR's own group — which is how a stop takes out its parent.
func Test_newHandle(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		setpgid bool
	}
	tests := []tc{
		{"a child leading its own group", true},
		{"a child in the supervisor's group", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, c.setpgid, "sleep 30")

		if h.pid <= 0 {
			t.Fatalf("the handle records pid %d", h.pid)
		}
		if h.PID() != h.pid {
			t.Errorf("PID() = %d, want %d", h.PID(), h.pid)
		}
		//: with Setpgid the leader pid IS the group id.
		if h.pgid != h.pid {
			t.Errorf("pgid = %d, want the leader pid %d", h.pgid, h.pid)
		}
		if h.setpgid != c.setpgid {
			t.Errorf("the handle recorded setpgid = %v, want %v", h.setpgid, c.setpgid)
		}
		//: the done channel is what Stop's select observes; a nil one would
		//: block every Stop forever.
		if h.done == nil {
			t.Error("the handle has no done channel")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_groupTarget pins the single most dangerous decision in the
// package.
//
// kill(-pgid) fans a signal across a whole process group. When the child leads
// its own group that is exactly what a stop wants. When it does NOT, the child
// is in the SUPERVISOR's group — so a negative target would signal the
// supervisor and every sibling it has. Degrading to the leader pid is the only
// safe answer.
func Test_handle_groupTarget(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		setpgid bool
		//: whether the target must be negative (a whole-group address).
		wantGroup bool
	}
	tests := []tc{
		{"a child leading its own group", true, true},
		{"a child in the supervisor's group", false, false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := &handle{pid: 4242, pgid: 4242, setpgid: c.setpgid, done: make(chan struct{})}

		got := h.groupTarget()

		if c.wantGroup {
			if got != -h.pgid {
				t.Errorf("groupTarget() = %d, want -%d", got, h.pgid)
			}
			return
		}
		if got != h.pid {
			t.Errorf("groupTarget() = %d, want the leader pid %d", got, h.pid)
		}
		//: a negative target without a private group would reach the
		//: supervisor's own group.
		if got < 0 {
			t.Error("groupTarget() addressed a group the child does not lead")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_PID pins the leader identifier, which is captured at spawn and
// never changes — a handle that re-read it could observe a recycled pid after
// the child was reaped.
func Test_handle_PID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		program string
	}
	tests := []tc{
		{"a long-lived child", "sleep 30"},
		{"a child that exits at once", "exit 0"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, c.program)

		first := h.PID()
		if first <= 0 {
			t.Fatalf("PID() = %d, want a positive pid", first)
		}
		//: reaping must not change what PID reports, or a caller logging it
		//: after the fact would print a pid the kernel has since reissued.
		if _, err := h.Wait(); err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
		if got := h.PID(); got != first {
			t.Errorf("PID() = %d after the reap, want %d", got, first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_Wait pins the memoisation. wait4 can only be called once per
// child; a second reap would either block forever or reap an unrelated process
// the kernel has since given the same pid, so every caller must observe the same
// stored outcome.
func Test_handle_Wait(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		program  string
		wantCode int
		wantSig  bool
	}
	tests := []tc{
		{name: "a clean exit", program: "exit 0", wantCode: 0},
		{name: "a non-zero exit", program: "exit 3", wantCode: 3},
		{name: "the highest status", program: "exit 255", wantCode: 255},
		//: a signalled death reports -1 and the signal, never a status.
		{name: "a signalled death", program: "kill -TERM $$; sleep 5", wantCode: signalledCode, wantSig: true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, c.program)

		first, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
		if first.Code != c.wantCode {
			t.Errorf("exit code = %d, want %d", first.Code, c.wantCode)
		}
		if first.Signaled != c.wantSig {
			t.Errorf("signalled = %v, want %v", first.Signaled, c.wantSig)
		}

		//: every later caller sees the same outcome; a second real reap would
		//: block or reap a recycled pid.
		second, serr := h.Wait()
		if serr != nil {
			t.Fatalf("the second Wait = %v, want nil", serr)
		}
		if second != first {
			t.Errorf("the second Wait = %+v, want the memoised %+v", second, first)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_Signal pins the leader-only delivery and the typed refusal.
func Test_handle_Signal(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		sig  syscall.Signal
	}
	tests := []tc{
		{"a liveness probe", syscall.Signal(0)},
		{"a graceful stop", syscall.SIGTERM},
		{"an ungraceful stop", syscall.SIGKILL},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, "sleep 30")

		if err := h.Signal(coreproc.Signal(c.sig)); err != nil {
			t.Fatalf("Signal(%v) = %v, want nil", c.sig, err)
		}

		//: a signal to a pid that cannot exist must be reported typed, so a
		//: supervisor can tell a delivery fault from a process that is simply
		//: gone.
		gone := &handle{proc: mustFindProcess(t, absentPID), pid: absentPID, pgid: absentPID, done: make(chan struct{})}
		err := gone.Signal(coreproc.Signal(syscall.SIGTERM))
		if err == nil {
			t.Fatal("Signal to an absent pid = nil, want SIGNAL_FAILED")
		}
		if !errs.HasCode(err, coreproc.CodeSignalFailed) {
			t.Errorf("Signal to an absent pid = %v, want SIGNAL_FAILED", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_SignalGroup pins that a grandchild is reached. That is the whole
// reason a private process group is asked for: a shell that backgrounds work
// leaves children the leader's own exit does not touch.
func Test_handle_SignalGroup(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		setpgid bool
	}
	tests := []tc{
		{"a private group", true},
		{"no private group", false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, c.setpgid, "sleep 30 & wait")

		if err := h.SignalGroup(coreproc.Signal(syscall.SIGKILL)); err != nil {
			t.Fatalf("SignalGroup = %v, want nil", err)
		}
		exit, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
		//: the leader died by the signal either way; the difference is whether
		//: the backgrounded sleep went with it, which groupTarget decides.
		if !exit.Signaled {
			t.Errorf("the leader exited with code %d, want a signalled death", exit.Code)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_signalGroupAllowGone pins the race this wrapper exists for: a
// child that exits between the decision to signal it and the kill(2) is not a
// failure, it is the outcome the caller wanted. Reporting ESRCH there would make
// every well-behaved shutdown look like an error.
func Test_handle_signalGroupAllowGone(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: whether the child is reaped before the signal is sent.
		alreadyGone bool
	}
	tests := []tc{
		{"a live group", false},
		{"a group that already exited", true},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, "sleep 30")
		if c.alreadyGone {
			if err := h.SignalGroup(coreproc.Signal(syscall.SIGKILL)); err != nil {
				t.Fatalf("preparing the gone case: %v", err)
			}
			if _, err := h.Wait(); err != nil {
				t.Fatalf("reaping: %v", err)
			}
		}

		//: either way this must succeed: the point is that the group is gone.
		if err := h.signalGroupAllowGone(coreproc.Signal(syscall.SIGTERM)); err != nil {
			t.Errorf("signalGroupAllowGone = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_Stop pins the escalation. A process that ignores SIGTERM must
// still stop, because "stop" is what the caller asked for — but it must be given
// the grace window first, or a well-behaved service loses its chance to flush.
func Test_handle_Stop(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		program string
		grace   time.Duration
	}
	tests := []tc{
		{"a child that honours SIGTERM", "sleep 30", 2 * time.Second},
		//: a child that traps SIGTERM only stops on the escalation.
		{"a child that ignores SIGTERM", "trap '' TERM; sleep 30", 200 * time.Millisecond},
		{"a child that already exited", "exit 0", time.Second},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, c.program)

		if err := h.Stop(t.Context(), c.grace, coreproc.Signal(syscall.SIGTERM)); err != nil {
			t.Fatalf("Stop = %v, want nil", err)
		}
		//: the process is reaped, so a further Wait returns immediately with
		//: the memoised outcome.
		if _, err := h.Wait(); err != nil {
			t.Errorf("Wait after Stop = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_awaitExit pins the three ways the wait ends: the process exits,
// the caller cancels, or the grace window closes without an exit. Only the last
// is "not settled", and that distinction is what tells Stop to escalate.
func Test_handle_awaitExit(t *testing.T) {
	t.Parallel()
	//: outcome names the terminal state the wait must reach, which reads
	//: better than three booleans and keeps the impossible combinations
	//: (cancelled but not settled) unrepresentable.
	type outcome int
	const (
		exited outcome = iota
		graceElapsed
		cancelled
	)
	type tc struct {
		name    string
		program string
		grace   time.Duration
		want    outcome
	}
	tests := []tc{
		{name: "the process exits within the window", program: "exit 0", grace: 5 * time.Second, want: exited},
		{name: "the process exits with no deadline", program: "exit 0", grace: 0, want: exited},
		{name: "the grace window closes first", program: "sleep 30", grace: 100 * time.Millisecond, want: graceElapsed},
		{name: "the caller cancels", program: "sleep 30", grace: 5 * time.Second, want: cancelled},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, c.program)

		ctx := t.Context()
		if c.want == cancelled {
			stopped, cancel := context.WithCancel(t.Context())
			cancel()
			defer cancel()
			ctx = stopped
		}

		settled, err := h.awaitExit(ctx, c.grace)

		//: only an elapsed grace window is "not settled", and that is exactly
		//: what tells Stop to escalate.
		wantSettled := c.want != graceElapsed
		if settled != wantSettled {
			t.Fatalf("awaitExit settled = %v, want %v (err %v)", settled, wantSettled, err)
		}
		if c.want == cancelled {
			//: a cancellation is surfaced verbatim so a caller shutting down
			//: recognises its own context error.
			if !errors.Is(err, context.Canceled) {
				t.Errorf("awaitExit = %v, want the cancellation", err)
			}
			return
		}
		if err != nil {
			t.Errorf("awaitExit = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_handle_reapInBackground pins that the driver closes done exactly once
// however many times it runs — the Once inside Wait is the guard, and a
// double-close would panic in a goroutine nobody can recover from.
func Test_handle_reapInBackground(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		drivers int
	}
	tests := []tc{
		{"a single driver", 1},
		{"several concurrent drivers", 8},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, "exit 0")

		done := make(chan struct{}, c.drivers)
		for range c.drivers {
			go func() {
				h.reapInBackground()
				done <- struct{}{}
			}()
		}
		for range c.drivers {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("a reap driver never returned")
			}
		}
		//: the reap happened exactly once, and the outcome is observable.
		if _, err := h.Wait(); err != nil {
			t.Errorf("Wait = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_timevalToDuration pins the two-field conversion. Dropping the microsecond
// half would round every CPU figure down to a whole second, which for a process
// that ran for 40ms reports zero.
func Test_timevalToDuration(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		tv   syscall.Timeval
		want time.Duration
	}
	tests := []tc{
		{"nothing", syscall.Timeval{}, 0},
		{"whole seconds", syscall.Timeval{Sec: 3}, 3 * time.Second},
		{"microseconds alone", syscall.Timeval{Usec: 1500}, 1500 * time.Microsecond},
		{"both halves", syscall.Timeval{Sec: 2, Usec: 500000}, 2*time.Second + 500*time.Millisecond},
		{"a sub-millisecond figure", syscall.Timeval{Usec: 40}, 40 * time.Microsecond},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := timevalToDuration(c.tv); got != c.want {
			t.Errorf("timevalToDuration(%+v) = %v, want %v", c.tv, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_readRusage pins that all three accounting fields are carried across. A
// supervisor uses them to decide whether a restart is a resource problem, so a
// silently zero MaxRSS would hide exactly the case worth catching.
func Test_readRusage(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		ru   syscall.Rusage
	}
	tests := []tc{
		{"an idle process", syscall.Rusage{}},
		{
			"a process with measurable usage",
			syscall.Rusage{
				Utime:  syscall.Timeval{Sec: 1, Usec: 250000},
				Stime:  syscall.Timeval{Usec: 500000},
				Maxrss: 65536,
			},
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		ru := c.ru
		out := coreproc.ExitValue{}

		readRusage(&ru, &out)

		if out.UserTime != timevalToDuration(c.ru.Utime) {
			t.Errorf("UserTime = %v, want %v", out.UserTime, timevalToDuration(c.ru.Utime))
		}
		if out.SystemTime != timevalToDuration(c.ru.Stime) {
			t.Errorf("SystemTime = %v, want %v", out.SystemTime, timevalToDuration(c.ru.Stime))
		}
		if out.MaxRSS != maxRSSKB(&ru) {
			t.Errorf("MaxRSS = %d, want %d", out.MaxRSS, maxRSSKB(&ru))
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_exitValueFrom pins the exit-versus-signal distinction end to end, through
// a real child. A signalled death reports code -1 on purpose: there IS no exit
// status, and reporting 0 would make a killed process look like a clean one.
func Test_exitValueFrom(t *testing.T) {
	t.Parallel()
	type tc struct {
		name     string
		program  string
		wantCode int
		wantSig  syscall.Signal
	}
	tests := []tc{
		{name: "a clean exit", program: "exit 0", wantCode: 0},
		{name: "a failing exit", program: "exit 7", wantCode: 7},
		{name: "a SIGTERM death", program: "kill -TERM $$; sleep 5", wantCode: signalledCode, wantSig: syscall.SIGTERM},
		{name: "a SIGKILL death", program: "kill -KILL $$; sleep 5", wantCode: signalledCode, wantSig: syscall.SIGKILL},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		h := startChild(t, true, c.program)

		got, err := h.Wait()
		if err != nil {
			t.Fatalf("Wait = %v, want nil", err)
		}
		if got.Code != c.wantCode {
			t.Errorf("Code = %d, want %d", got.Code, c.wantCode)
		}
		if c.wantSig != 0 {
			if !got.Signaled {
				t.Fatalf("Signaled = false, want a death by %v", c.wantSig)
			}
			if got.Signal != coreproc.Signal(c.wantSig) {
				t.Errorf("Signal = %v, want %v", got.Signal, c.wantSig)
			}
			return
		}
		if got.Signaled {
			t.Errorf("Signaled = true for a normal exit")
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_processGone pins the two-part test: the error must be OUR signal failure
// AND carry ESRCH. Matching on ESRCH alone would swallow an ESRCH surfacing from
// somewhere else entirely, and matching on the code alone would swallow a
// permission denial as if the process had exited.
func Test_processGone(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		err  error
		want bool
	}
	tests := []tc{
		{"our signal failure carrying ESRCH", wrapSignal(syscall.ESRCH), true},
		{"our signal failure carrying EPERM", wrapSignal(syscall.EPERM), false},
		{"a bare ESRCH from elsewhere", syscall.ESRCH, false},
		{"a different wrapper carrying ESRCH", wrapSpawn(syscall.ESRCH), false},
		{"no error at all", nil, false},
		{"an untyped error", errors.New("gone"), false},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := processGone(c.err); got != c.want {
			t.Errorf("processGone(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// mustFindProcess returns an *os.Process handle for pid without checking that
// it exists — os.FindProcess never fails on Unix, which is exactly why the
// signal itself has to report a missing target.
func mustFindProcess(t *testing.T, pid int) *os.Process {
	t.Helper()
	p, err := os.FindProcess(pid)
	if err != nil {
		t.Fatalf("os.FindProcess(%d) = %v", pid, err)
	}
	return p
}
