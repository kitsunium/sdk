package service

import (
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// policies are Restart='s seven values in Table 1's column order.
var policies = [7]coresvc.Restart{
	coresvc.RestartNo, coresvc.RestartOnSuccess, coresvc.RestartOnFailure, coresvc.RestartOnAbnormal,
	coresvc.RestartOnWatchdog, coresvc.RestartOnAbort, coresvc.RestartAlways,
}

// TestRestartTable is Table 1 as a function, the rows S0b cannot reach by
// events included (OOM, resources, protocol, skip, the limiter).
func TestRestartTable(t *testing.T) {
	rows := []struct {
		result coresvc.Result
		want   [7]bool
	}{
		{coresvc.ResultSuccess, [7]bool{false, true, false, false, false, false, true}},
		{coresvc.ResultExitCode, [7]bool{false, false, true, false, false, false, true}},
		{coresvc.ResultSignal, [7]bool{false, false, true, true, false, true, true}},
		{coresvc.ResultCoreDump, [7]bool{false, false, true, true, false, true, true}},
		{coresvc.ResultTimeout, [7]bool{false, false, true, true, false, false, true}},
		{coresvc.ResultWatchdog, [7]bool{false, false, true, true, true, false, true}},
		{coresvc.ResultOOMKill, [7]bool{false, false, true, true, false, false, true}},
		{coresvc.ResultResources, [7]bool{false, false, true, true, false, false, true}},
		{coresvc.ResultProtocol, [7]bool{false, false, true, true, false, false, true}},
		{coresvc.ResultExecCondition, [7]bool{}},
		{coresvc.ResultStartLimitHit, [7]bool{}},
	}
	for _, row := range rows {
		for col, policy := range policies {
			if got := restartTable(policy, row.result); got != row.want[col] {
				t.Errorf("restartTable(%s, %s) = %v, want %v", policy, row.result, got, row.want[col])
			}
		}
	}
}

// TestExitStatusSets: RestartPreventExitStatus= never restarts,
// RestartForceExitStatus= always does — but a oneshot that succeeded —, and
// SuccessExitStatus= makes a status clean.
func TestExitStatusSets(t *testing.T) {
	segv := coreproc.Signal(syscall.SIGSEGV)
	s := &settings{
		typ: coresvc.TypeSimple, restart: coresvc.RestartAlways,
		prevent: coresvc.ExitStatusValue{Codes: []int{75}, Signals: []coreproc.Signal{segv}},
		force:   coresvc.ExitStatusValue{Codes: []int{3}},
		success: coresvc.ExitStatusValue{Codes: []int{250}},
	}
	if shallRestart(s, coresvc.ResultExitCode, coreproc.ExitValue{Code: 75}, true) {
		t.Error("a prevented exit code restarted")
	}
	if shallRestart(s, coresvc.ResultSignal, coreproc.ExitValue{Signal: segv, Signaled: true}, true) {
		t.Error("a prevented signal restarted")
	}
	s.restart = coresvc.RestartNo
	if !shallRestart(s, coresvc.ResultExitCode, coreproc.ExitValue{Code: 3}, true) {
		t.Error("a forced exit code did not restart under Restart=no")
	}
	s.typ = coresvc.TypeOneshot
	if shallRestart(s, coresvc.ResultSuccess, coreproc.ExitValue{Code: 3}, true) {
		t.Error("a oneshot that succeeded restarted on a forced status")
	}
	if got := classifyMain(s, coreproc.ExitValue{Code: 250}, false, 0); got != coresvc.ResultSuccess {
		t.Errorf("SuccessExitStatus=250: %s", got)
	}
	// A oneshot's SIGTERM is unclean; a daemon's is clean.
	term := coreproc.ExitValue{Signal: coreproc.Signal(syscall.SIGTERM), Signaled: true}
	if got := classifyMain(s, term, false, 0); got != coresvc.ResultSignal {
		t.Errorf("oneshot SIGTERM: %s", got)
	}
	s.typ = coresvc.TypeSimple
	if got := classifyMain(s, term, false, 0); got != coresvc.ResultSuccess {
		t.Errorf("daemon SIGTERM: %s", got)
	}
	if got := classifyMain(s, coreproc.ExitValue{Code: 9}, false, coresvc.FlagIgnoreFailure); got != coresvc.ResultSuccess {
		t.Errorf(`"-" exit 9: %s`, got)
	}
	if got := classifyMain(s, coreproc.ExitValue{}, true, 0); got != coresvc.ResultSuccess {
		t.Errorf("an adopted process gone without a status: %s", got)
	}
}

// TestRestartExceptions: a stop the caller asked for never restarts.
func TestRestartExceptions(t *testing.T) {
	h := newHarness(t, Config{Restart: coresvc.RestartAlways, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	h.start()
	main := h.m.mainPID
	stop := h.request(evStop)
	h.kill(main, coreproc.Signal(syscall.SIGTERM))
	h.want(coresvc.StateInactive, coresvc.SubDead)
	h.wantReply(stop, 0)
}

// TestRestartDelayManualExample is systemd.service(5)'s RestartSteps= example:
// RestartSec=10s, RestartSteps=4, RestartMaxDelaySec=160s restart after 10, 20,
// 40, 80, 160, 160, 160 s — on the kernel's backoff curve.
func TestRestartDelayManualExample(t *testing.T) {
	s := &settings{restartDelay: 10 * time.Second, restartSteps: 4, restartMax: 160 * time.Second}
	want := []time.Duration{10, 20, 40, 80, 160, 160, 160}
	for i, w := range want {
		if got := restartDelay(s, uint32(i+1)); got != w*time.Second {
			t.Errorf("restart %d: %s, want %s", i+1, got, w*time.Second)
		}
	}
	// The same, through the reducer: each crash's restart delay.
	h := newHarness(t, Config{
		Restart: coresvc.RestartAlways, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
		RestartDelay: 10 * time.Second, RestartSteps: 4, RestartMaxDelay: 160 * time.Second,
		StartLimitInterval: coresvc.Zero,
	})
	h.start()
	for i, w := range want {
		h.exit(h.m.mainPID, 1)
		h.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
		if got := h.deadline(timerState); got != w*time.Second {
			t.Fatalf("crash %d: restart in %s, want %s", i+1, got, w*time.Second)
		}
		h.advance(w * time.Second)
		h.want(coresvc.StateActive, coresvc.SubRunning)
	}
	// RESTART_RESET=1 needs NotifyAccess; a caller's start flushes too.
	if h.m.restarts != uint32(len(want)) {
		t.Fatalf("restarts = %d", h.m.restarts)
	}
}

// TestRestartDelay: the default 100 ms, coresvc.Zero at once, no curve without
// steps.
func TestRestartDelay(t *testing.T) {
	for _, tc := range []struct {
		value time.Duration
		want  time.Duration
	}{{0, 100 * time.Millisecond}, {coresvc.Zero, 0}, {3 * time.Second, 3 * time.Second}} {
		h := newHarness(t, Config{
			Restart: coresvc.RestartOnFailure, RestartDelay: tc.value,
			ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
		})
		h.start()
		h.exit(h.m.mainPID, 1)
		if got := h.deadline(timerState); got != tc.want {
			t.Errorf("RestartSec %s: %s, want %s", tc.value, got, tc.want)
		}
	}
}

// TestRestartRandomizedDelay: RestartRandomizedDelaySec= adds a draw in
// [0, bound] — the shell's draw, carried by the event.
func TestRestartRandomizedDelay(t *testing.T) {
	h := newHarness(t, Config{
		Restart: coresvc.RestartOnFailure, RestartDelay: time.Second,
		RestartRandomizedDelay: time.Second, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	})
	h.start()
	h.rand = 250_000_123
	h.exit(h.m.mainPID, 1)
	if got, want := h.deadline(timerState), time.Second+250_000_123; got != want {
		t.Fatalf("delay %s, want %s", got, want)
	}
	if got := jitter(^uint64(0), time.Second); got < 0 || got > time.Second {
		t.Fatalf("jitter out of its bound: %s", got)
	}
}

// TestRestartMode: normal publishes the failure before auto-restart, direct
// does not, debug sets DEBUG_INVOCATION=1 on the restarted unit.
func TestRestartMode(t *testing.T) {
	for _, mode := range []coresvc.RestartMode{coresvc.RestartModeNormal, coresvc.RestartModeDirect, coresvc.RestartModeDebug} {
		h := newHarness(t, Config{
			Restart: coresvc.RestartOnFailure, RestartMode: mode,
			ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
		})
		h.start()
		h.published = nil
		h.exit(h.m.mainPID, 1)
		sawFailed := false
		for _, s := range h.publishedStates() {
			sawFailed = sawFailed || s == coresvc.StateFailed
		}
		if sawFailed != (mode != coresvc.RestartModeDirect) {
			t.Errorf("RestartMode=%s published failed: %v (%v)", mode, sawFailed, h.publishedStates())
		}
		h.advance(time.Second)
		debug := hasVar(h.lastSpawn(), "DEBUG_INVOCATION=1")
		if debug != (mode == coresvc.RestartModeDebug) {
			t.Errorf("RestartMode=%s: DEBUG_INVOCATION=1 %v", mode, debug)
		}
	}
}

// TestStartLimit: every start counts, the manual ones included; past the
// burst the unit is failed with start-limit-hit and not restarted; the window
// recovers after its interval, never under Infinity, and ResetFailed flushes
// it.
func TestStartLimit(t *testing.T) {
	newLimited := func(interval time.Duration) *harness {
		return newHarness(t, Config{
			Restart: coresvc.RestartAlways, StartLimitBurst: 3, StartLimitInterval: interval,
			RestartDelay: time.Second, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
		})
	}
	h := newLimited(10 * time.Second)
	h.wantReply(h.start(), 0) // manual start: 1
	h.exit(h.m.mainPID, 1)    // auto-restart in 1 s
	h.advance(time.Second)    // start 2
	h.exit(h.m.mainPID, 1)
	h.advance(time.Second) // start 3
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.exit(h.m.mainPID, 1)
	h.advance(time.Second) // start 4: refused
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	if h.m.result != coresvc.ResultStartLimitHit {
		t.Fatalf("result %s, want start-limit-hit", h.m.result)
	}
	h.wantReply(h.start(), coresvc.CodeStartLimitHit) // still within the window
	h.advance(11 * time.Second)
	h.wantReply(h.start(), 0) // the window is over

	inf := newLimited(coresvc.Infinity)
	for range 3 {
		inf.wantReply(inf.start(), 0)
		inf.wantReply(inf.stopAndExit(), 0)
		inf.advance(time.Hour)
	}
	inf.wantReply(inf.start(), coresvc.CodeStartLimitHit)
	inf.wantReply(inf.request(evResetFailed), 0)
	inf.want(coresvc.StateInactive, coresvc.SubDead)
	inf.wantReply(inf.start(), 0)

	off := newLimited(coresvc.Zero)
	for range 10 {
		off.wantReply(off.start(), 0)
		off.wantReply(off.stopAndExit(), 0)
	}
}
