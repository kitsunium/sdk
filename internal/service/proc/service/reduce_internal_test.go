//go:build !windows

package service

import (
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// TestTypes: simple, exec and idle are up once spawned; the zero Type is
// simple with an ExecStart= and oneshot without; a spawn failure fails the
// start with the result resources.
func TestTypes(t *testing.T) {
	for _, typ := range []coresvc.Type{coresvc.TypeDefault, coresvc.TypeSimple, coresvc.TypeExec, coresvc.TypeIdle} {
		h := newHarness(t, Config{Type: typ, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
		id := h.start()
		h.want(coresvc.StateActive, coresvc.SubRunning)
		h.wantReply(id, 0)
		if h.m.mainPID == 0 {
			t.Fatalf("Type=%q: no main process", typ)
		}
	}
	h := newHarness(t, Config{RemainAfterExit: true, ExecStop: []coresvc.CommandValue{cmd(t, "/bin/stop")}})
	if h.m.s.typ != coresvc.TypeOneshot {
		t.Fatalf("no ExecStart=: Type=%s, want oneshot", h.m.s.typ)
	}
	f := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/missing")}})
	f.failSpawn[listStart] = true
	f.wantReply(f.start(), coresvc.CodeStartFailed)
	f.want(coresvc.StateFailed, coresvc.SubFailed)
	if f.m.result != coresvc.ResultResources {
		t.Fatalf("result %s, want resources", f.m.result)
	}
}

// TestOneshotSequence: a oneshot runs its ExecStart= one after the other, is
// up once the last succeeded, then stops: its start job answers nil.
func TestOneshotSequence(t *testing.T) {
	h := newHarness(t, Config{
		Type:      coresvc.TypeOneshot,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/a"), cmd(t, "/bin/b")},
	})
	id := h.start()
	h.want(coresvc.StateActivating, coresvc.SubStart)
	h.exit(h.m.mainPID, 0)
	if got := h.lastSpawn().cmd.Path; got != "/bin/b" {
		t.Fatalf("second command %q", got)
	}
	h.wantWaiting(id)
	h.exit(h.m.mainPID, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	h.wantReply(id, 0)
	// A failure of the second stops the sequence.
	f := newHarness(t, Config{
		Type:      coresvc.TypeOneshot,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/a"), cmd(t, "/bin/b"), cmd(t, "/bin/c")},
	})
	fid := f.start()
	f.exit(f.m.mainPID, 0)
	f.exit(f.m.mainPID, 2)
	f.want(coresvc.StateFailed, coresvc.SubFailed)
	f.wantReply(fid, coresvc.CodeStartFailed)
	if n := len(f.spawnsOf(listStart)); n != 2 {
		t.Fatalf("%d main commands ran, want 2", n)
	}
}

// TestOneshotRemainAfterExit: exited, then ExecStop= on a stop; Restart=always
// and on-success are refused for a oneshot.
func TestOneshotRemainAfterExit(t *testing.T) {
	h := newHarness(t, Config{
		Type: coresvc.TypeOneshot, RemainAfterExit: true,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/a")}, ExecStop: []coresvc.CommandValue{cmd(t, "/bin/undo")},
	})
	id := h.start()
	h.exit(h.m.mainPID, 0)
	h.want(coresvc.StateExited, coresvc.SubExited)
	h.wantReply(id, 0)
	stop := h.request(evStop)
	h.want(coresvc.StateDeactivating, coresvc.SubStop)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	h.wantReply(stop, 0)
	for _, r := range []coresvc.Restart{coresvc.RestartAlways, coresvc.RestartOnSuccess} {
		_, err := resolve(&Config{Name: "u", Type: coresvc.TypeOneshot, Restart: r, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/a")}})
		if !hasCode(err, coresvc.CodeConfigInvalid) {
			t.Errorf("oneshot Restart=%s: %v", r, err)
		}
	}
}

// TestExecChain: ExecStartPre= in order, then the main process, then
// ExecStartPost=; a failing pre-start skips ExecStart= and ExecStop= but runs
// ExecStopPost=; "-" reads a failure as a success.
func TestExecChain(t *testing.T) {
	cfg := Config{
		ExecStartPre:  []coresvc.CommandValue{cmd(t, "/bin/pre1"), cmd(t, "-/bin/pre2")},
		ExecStart:     []coresvc.CommandValue{cmd(t, "/bin/d")},
		ExecStartPost: []coresvc.CommandValue{cmd(t, "/bin/post")},
		ExecStop:      []coresvc.CommandValue{cmd(t, "/bin/stop")},
		ExecStopPost:  []coresvc.CommandValue{cmd(t, "/bin/cleanup")},
	}
	h := newHarness(t, cfg)
	id := h.start()
	h.want(coresvc.StateActivating, coresvc.SubStartPre)
	h.exit(h.m.controlPID, 0)
	h.exit(h.m.controlPID, 7) // "-": ignored
	h.want(coresvc.StateActivating, coresvc.SubStartPost)
	if !hasVar(h.lastSpawn(), "MAINPID="+strconv.Itoa(h.m.mainPID)) {
		t.Fatalf("ExecStartPost= without $MAINPID: %v", h.lastSpawn().vars)
	}
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)

	f := newHarness(t, cfg)
	fid := f.start()
	f.exit(f.m.controlPID, 1)
	f.want(coresvc.StateDeactivating, coresvc.SubStopPost)
	f.exit(f.m.controlPID, 0)
	f.want(coresvc.StateFailed, coresvc.SubFailed)
	f.wantReply(fid, coresvc.CodeStartFailed)
	if len(f.spawnsOf(listStart)) != 0 || len(f.spawnsOf(listStop)) != 0 {
		t.Fatal("ExecStart= or ExecStop= ran after a failed pre-start")
	}
}

// TestExecCondition: 0 goes on; 1 to 254 skips — condition-skipped, the
// post-stop run, no restart even under always —; 255 fails, so always
// restarts it.
func TestExecCondition(t *testing.T) {
	for _, tc := range []struct {
		code  int
		state coresvc.State
	}{{0, coresvc.StateActive}, {1, coresvc.StateConditionSkipped}, {254, coresvc.StateConditionSkipped}, {255, coresvc.StateAutoRestart}} {
		h := newHarness(t, Config{
			Restart:       coresvc.RestartAlways,
			ExecCondition: []coresvc.CommandValue{cmd(t, "/bin/check")},
			ExecStart:     []coresvc.CommandValue{cmd(t, "/bin/d")},
			ExecStopPost:  []coresvc.CommandValue{cmd(t, "/bin/cleanup")},
		})
		id := h.start()
		h.want(coresvc.StateActivating, coresvc.SubCondition)
		h.exit(h.m.controlPID, tc.code)
		if tc.code != 0 {
			h.want(coresvc.StateDeactivating, coresvc.SubStopPost)
			h.exit(h.m.controlPID, 0)
		}
		if h.m.state != tc.state {
			t.Fatalf("ExecCondition= exit %d: %s/%s, want %s", tc.code, h.m.state, h.m.sub, tc.state)
		}
		if tc.state == coresvc.StateConditionSkipped {
			h.wantReply(id, 0)
		}
	}
}

// TestExecStopOnlyAfterAStart: ExecStop= runs with $MAINPID after a
// successful start, and never after a failed one.
func TestExecStopOnlyAfterAStart(t *testing.T) {
	cfg := Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, ExecStop: []coresvc.CommandValue{cmd(t, "/bin/stop")}}
	h := newHarness(t, cfg)
	h.start()
	main := h.m.mainPID
	stop := h.request(evStop)
	h.want(coresvc.StateDeactivating, coresvc.SubStop)
	if !hasVar(h.lastSpawn(), "MAINPID="+strconv.Itoa(main)) {
		t.Fatalf("ExecStop= without $MAINPID: %v", h.lastSpawn().vars)
	}
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	h.kill(main, sigTERM)
	h.wantReply(stop, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
}

// TestExecStopPostEnv: ExecStopPost= gets $SERVICE_RESULT, $EXIT_CODE and
// $EXIT_STATUS.
func TestExecStopPostEnv(t *testing.T) {
	h := newHarness(t, Config{
		ExecStart:    []coresvc.CommandValue{cmd(t, "/bin/d")},
		ExecStopPost: []coresvc.CommandValue{cmd(t, "/bin/cleanup")},
	})
	h.start()
	h.kill(h.m.mainPID, coreproc.Signal(syscall.SIGSEGV))
	post := h.lastSpawn()
	for _, v := range []string{"SERVICE_RESULT=signal", "EXIT_CODE=killed", "EXIT_STATUS=SEGV"} {
		if !hasVar(post, v) {
			t.Errorf("ExecStopPost= lacks %s: %v", v, post.vars)
		}
	}
}

// TestTimeouts: a start that does not end within TimeoutStartSec= is
// terminated; a stop that does not end within TimeoutStopSec= is killed;
// EXTEND_TIMEOUT_USEC= pushes the deadline out.
func TestTimeouts(t *testing.T) {
	h := newHarness(t, Config{
		Type: coresvc.TypeNotify, Notify: stubNotify{}, TimeoutStart: 5 * time.Second,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	})
	id := h.start()
	main := h.m.mainPID
	h.advance(4 * time.Second)
	h.notify(main, "EXTEND_TIMEOUT_USEC=3000000")
	h.advance(2 * time.Second) // 6 s: the original deadline passed, the extension holds
	h.want(coresvc.StateActivating, coresvc.SubStart)
	h.advance(time.Second) // 7 s
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	if got := h.sentTo(main); len(got) == 0 || got[0] != sigTERM {
		t.Fatalf("signals %v, want SIGTERM first", got)
	}
	h.kill(main, sigTERM)
	h.wantReply(id, coresvc.CodeStartFailed)
	if h.m.result != coresvc.ResultTimeout {
		t.Fatalf("result %s, want timeout", h.m.result)
	}

	s := newHarness(t, Config{TimeoutStop: 10 * time.Second, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	s.start()
	main = s.m.mainPID
	stop := s.request(evStop)
	s.advance(10 * time.Second)
	s.want(coresvc.StateDeactivating, coresvc.SubStopSigkill)
	if got := s.sentTo(main); got[len(got)-1] != sigKILL {
		t.Fatalf("signals %v, want SIGKILL last", got)
	}
	s.kill(main, sigKILL)
	s.wantReply(stop, 0)
	s.want(coresvc.StateFailed, coresvc.SubFailed)
}

// TestTimeoutModes: TimeoutStartFailureMode= — terminate sends KillSignal=,
// abort WatchdogSignal= under TimeoutAbortSec=, kill FinalKillSignal= at once.
func TestTimeoutModes(t *testing.T) {
	for _, tc := range []struct {
		mode coresvc.TimeoutMode
		sig  coreproc.Signal
		sub  coresvc.Sub
	}{
		{coresvc.TimeoutTerminate, sigTERM, coresvc.SubStopSigterm},
		{coresvc.TimeoutAbort, sigABRT, coresvc.SubStopWatchdog},
		{coresvc.TimeoutKill, sigKILL, coresvc.SubStopSigkill},
	} {
		h := newHarness(t, Config{
			Type: coresvc.TypeNotify, Notify: stubNotify{}, TimeoutStart: time.Second,
			TimeoutAbort: 7 * time.Second, TimeoutStartFailureMode: tc.mode, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
		})
		h.start()
		h.advance(time.Second)
		h.want(coresvc.StateDeactivating, tc.sub)
		if got := h.sentTo(h.m.mainPID); got[0] != tc.sig {
			t.Errorf("mode %s: signals %v, want %s first", tc.mode, got, tc.sig)
		}
		if tc.mode == coresvc.TimeoutAbort && h.deadline(timerState) != 7*time.Second {
			t.Errorf("abort waits %s, want TimeoutAbortSec=", h.deadline(timerState))
		}
	}
}

// TestRuntimeMax: RuntimeMaxSec= stops the unit with the result timeout,
// RuntimeRandomizedExtraSec= extending it by the shell's draw.
func TestRuntimeMax(t *testing.T) {
	h := newHarness(t, Config{
		RuntimeMax: time.Minute, RuntimeRandomizedExtra: time.Second,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	})
	h.rand = 500
	h.start()
	if got := h.deadline(timerState); got != time.Minute+500 {
		t.Fatalf("runtime limit in %s", got)
	}
	main := h.m.mainPID
	h.advance(time.Minute + 500)
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	h.kill(main, sigTERM)
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	if h.m.result != coresvc.ResultTimeout {
		t.Fatalf("result %s", h.m.result)
	}
}

// TestWatchdogKeepAlive: WATCHDOG=1 re-arms the deadline; a missed one sends
// WatchdogSignal=, then FinalKillSignal= after TimeoutAbortSec=; the run ends
// with the result watchdog and on-watchdog restarts it.
func TestWatchdogKeepAlive(t *testing.T) {
	h := newHarness(t, Config{
		Type: coresvc.TypeNotify, Notify: stubNotify{}, Watchdog: 10 * time.Second,
		TimeoutAbort: 5 * time.Second, Restart: coresvc.RestartOnWatchdog,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	})
	h.start()
	main := h.m.mainPID
	if !hasVar(h.spawnsOf(listStart)[0], "WATCHDOG_USEC=10000000") {
		t.Fatalf("no $WATCHDOG_USEC: %v", h.spawnsOf(listStart)[0].vars)
	}
	h.notify(main, "READY=1")
	h.advance(8 * time.Second)
	h.notify(main, "WATCHDOG=1")
	h.advance(8 * time.Second) // 16 s: alive thanks to the ping at 8 s
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.advance(2 * time.Second) // 18 s: missed
	h.want(coresvc.StateDeactivating, coresvc.SubStopWatchdog)
	if got := h.sentTo(main); len(got) != 1 || got[0] != sigABRT {
		t.Fatalf("signals %v, want SIGABRT", got)
	}
	h.advance(5 * time.Second) // TimeoutAbortSec=
	if got := h.sentTo(main); got[len(got)-1] != sigKILL {
		t.Fatalf("signals %v, want SIGKILL after the abort timeout", got)
	}
	h.kill(main, sigKILL)
	h.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	if h.m.result != coresvc.ResultWatchdog {
		t.Fatalf("result %s", h.m.result)
	}
}

// TestWatchdogTriggerAndOverride: WATCHDOG=trigger aborts at once, even
// without WatchdogSec=; WATCHDOG_USEC= sets a new period.
func TestWatchdogTriggerAndOverride(t *testing.T) {
	h := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	h.start()
	main := h.m.mainPID
	h.notify(main, "READY=1")
	h.notify(main, "WATCHDOG_USEC=3000000")
	if got := h.deadline(timerWatchdog); got != 3*time.Second {
		t.Fatalf("WATCHDOG_USEC= armed %s", got)
	}
	h.notify(main, "WATCHDOG=trigger")
	h.want(coresvc.StateDeactivating, coresvc.SubStopWatchdog)
	h.kill(main, sigABRT)
	if h.m.result != coresvc.ResultWatchdog {
		t.Fatalf("result %s", h.m.result)
	}
}

// TestNotifyReady: READY=1 from the main process is up; from another one it
// is refused and counted; STATUS=, ERRNO=, BUSERROR=, VARLINKERROR= and
// EXIT_STATUS= are recorded; a main process that ends before READY=1 is the
// result protocol.
func TestNotifyReady(t *testing.T) {
	h := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	id := h.start()
	main := h.m.mainPID
	h.notify(main+1000, "READY=1")
	h.want(coresvc.StateActivating, coresvc.SubStart)
	if h.m.notifyRefused != 1 {
		t.Fatalf("refused %d", h.m.notifyRefused)
	}
	h.feed(event{kind: evNotify, msg: coresvc.NotifyValue{Fields: map[string]string{"READY": "1"}, PID: main}})
	if h.m.notifyRefused != 2 || h.m.sub != coresvc.SubStart {
		t.Fatal("an unverified sender was read")
	}
	h.notify(main, "READY=1", "STATUS=serving", "ERRNO=2", "BUSERROR=org.x.E", "VARLINKERROR=io.x.E", "EXIT_STATUS=3")
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	snap := snapshot(&h.m)
	if snap.Status != "serving" || snap.Errno != 2 || snap.BusError != "org.x.E" || snap.VarlinkError != "io.x.E" || snap.ExitStatusText != "3" {
		t.Fatalf("metadata %+v", snap)
	}
	p := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	pid := p.start()
	p.exit(p.m.mainPID, 0)
	p.wantReply(pid, coresvc.CodeStartFailed)
	if p.m.result != coresvc.ResultProtocol {
		t.Fatalf("result %s, want protocol", p.m.result)
	}
}

// TestNotifyStopping: STOPPING=1 is deactivating, no signal sent, under
// TimeoutStopSec=.
func TestNotifyStopping(t *testing.T) {
	h := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	h.start()
	main := h.m.mainPID
	h.notify(main, "READY=1")
	h.notify(main, "STOPPING=1")
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	if len(h.sentTo(main)) != 0 {
		t.Fatal("STOPPING=1 sent a signal")
	}
	h.exit(main, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
}

// TestNotifyMainPID: MAINPID= is accepted for a process of the unit's cgroup
// the attacher adopts, and refused for PID 1, the engine, a process outside
// the cgroup, or without an attacher; without a cgroup only a privileged
// sender is believed.
func TestNotifyMainPID(t *testing.T) {
	cfg := Config{
		Type: coresvc.TypeNotify, Notify: stubNotify{}, Cgroup: stubCgroup{}, Attacher: stubAttacher{},
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	}
	h := newHarness(t, cfg)
	h.start()
	main := h.m.mainPID
	h.notify(main, "MAINPID=4711", "READY=1")
	if h.m.mainPID != 4711 || !slices.Contains(h.forgotten, main) {
		t.Fatalf("main %d, forgotten %v", h.m.mainPID, h.forgotten)
	}
	refused := h.m.notifyRefused
	for _, v := range []string{"1", strconv.Itoa(h.m.s.selfPID), "x"} {
		h.notify(4711, "MAINPID="+v)
	}
	h.feed(event{kind: evNotify, msg: coresvc.NotifyValue{Fields: map[string]string{"MAINPID": "5000"}, PID: 4711, Verified: true}, flags: flagMember})
	if h.m.notifyRefused != refused+4 || h.m.mainPID != 4711 {
		t.Fatalf("refused %d (+%d), main %d", h.m.notifyRefused, h.m.notifyRefused-refused, h.m.mainPID)
	}
	n := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	n.start()
	n.notify(n.m.mainPID, "MAINPID=4711")
	if n.m.notifyRefused != 1 || len(n.attaches) != 0 {
		t.Fatal("MAINPID= accepted without an attacher")
	}
	cfg.Cgroup = nil
	r := newHarness(t, cfg)
	r.start()
	main = r.m.mainPID
	r.notify(main, "MAINPID=4711")
	if len(r.attaches) != 0 {
		t.Fatal("an unprivileged sender moved the main process without a cgroup")
	}
	r.feed(event{kind: evNotify, msg: coresvc.NotifyValue{Fields: map[string]string{"MAINPID": "4711"}, PID: main, UID: 0, Verified: true}})
	if r.m.mainPID != 4711 {
		t.Fatal("a privileged sender's MAINPID= was refused")
	}
}

// TestNotifyAccess: the zero is main for a notify type or a watchdog; exec and
// all need a cgroup; exec admits the command running; NOTIFYACCESS= overrides.
func TestNotifyAccess(t *testing.T) {
	w := newHarness(t, Config{Notify: stubNotify{}, Watchdog: time.Second, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	if w.m.s.notifyAccess != coresvc.NotifyAccessMain {
		t.Fatalf("WatchdogSec= implies %s", w.m.s.notifyAccess)
	}
	_, err := resolve(&Config{Name: "u", NotifyAccess: coresvc.NotifyAccessAll, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	if !hasCode(err, coresvc.CodeDirectiveUnsupported) {
		t.Fatalf("all without a cgroup: %v", err)
	}
	h := newHarness(t, Config{
		Type: coresvc.TypeNotify, NotifyAccess: coresvc.NotifyAccessExec, Notify: stubNotify{}, Cgroup: stubCgroup{},
		ExecStartPost: []coresvc.CommandValue{cmd(t, "/bin/post")}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	})
	h.start()
	h.notify(h.m.mainPID, "READY=1")
	h.want(coresvc.StateActivating, coresvc.SubStartPost)
	if !hasVar(h.lastSpawn(), "NOTIFY_SOCKET=@test-notify") {
		t.Fatal("NotifyAccess=exec: a command without $NOTIFY_SOCKET")
	}
	h.notify(h.m.controlPID, "STATUS=from the command")
	if h.m.status != "from the command" {
		t.Fatal("NotifyAccess=exec refused the command running")
	}
	h.notify(h.m.mainPID, "NOTIFYACCESS=none")
	h.notify(h.m.mainPID, "STATUS=late")
	if h.m.status == "late" {
		t.Fatal("NOTIFYACCESS=none was not applied")
	}
}

// TestReloadCommands: ExecReload= with $MAINPID, then ExecReloadPost=; a
// failing reload command fails the reload, not the unit; a unit with no
// mechanism refuses the reload; a reload that times out kills its command.
func TestReloadCommands(t *testing.T) {
	cfg := Config{
		ExecStart:  []coresvc.CommandValue{cmd(t, "/bin/d")},
		ExecReload: []coresvc.CommandValue{cmd(t, "/bin/kill", "-HUP", "$MAINPID")}, ExecReloadPost: []coresvc.CommandValue{cmd(t, "/bin/after")},
	}
	h := newHarness(t, cfg)
	h.start()
	id := h.request(evReload)
	h.want(coresvc.StateReloading, coresvc.SubReload)
	if !hasVar(h.lastSpawn(), "MAINPID="+strconv.Itoa(h.m.mainPID)) {
		t.Fatal("ExecReload= without $MAINPID")
	}
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateReloading, coresvc.SubReloadPost)
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	bad := h.request(evReload)
	h.exit(h.m.controlPID, 1)
	h.wantReply(bad, coresvc.CodeReloadFailed)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	slow := h.request(evReload)
	ctl := h.m.controlPID
	h.advance(90 * time.Second)
	h.wantReply(slow, coresvc.CodeReloadFailed)
	if got := h.sentTo(ctl); len(got) != 1 || got[0] != sigKILL {
		t.Fatalf("the timed-out reload command got %v", got)
	}
	n := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	n.start()
	n.wantReply(n.request(evReload), coresvc.CodeReloadUnsupported)
}

// TestReloadNotifyProtocol: Type=notify-reload sends ReloadSignal=, waits for
// RELOADING=1 stamped after the request, then READY=1; a stale stamp is not
// the answer; RELOADING=1 and READY=1 at once complete it; an unasked
// RELOADING=1 is a reload of the unit's own.
func TestReloadNotifyProtocol(t *testing.T) {
	h := newHarness(t, Config{Type: coresvc.TypeNotifyReload, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	h.start()
	main := h.m.mainPID
	h.notify(main, "READY=1")
	id := h.request(evReload)
	h.want(coresvc.StateReloading, coresvc.SubReloadSignal)
	if got := h.sentTo(main); len(got) != 1 || got[0] != sigHUP {
		t.Fatalf("signals %v, want SIGHUP", got)
	}
	h.notify(main, "RELOADING=1", "MONOTONIC_USEC=999999") // before the request
	h.want(coresvc.StateReloading, coresvc.SubReloadSignal)
	h.notify(main, "RELOADING=1", "MONOTONIC_USEC=1000001")
	h.want(coresvc.StateReloading, coresvc.SubReloadNotify)
	h.notify(main, "READY=1")
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	both := h.request(evReload)
	h.notify(main, "RELOADING=1", "MONOTONIC_USEC=1000002", "READY=1")
	h.wantReply(both, 0)
	h.notify(main, "RELOADING=1")
	h.want(coresvc.StateReloading, coresvc.SubReloadNotify)
	h.notify(main, "READY=1")
	h.want(coresvc.StateActive, coresvc.SubRunning)
}

// TestKillModes: process signals the main process; none signals nothing and
// leaves it.
func TestKillModes(t *testing.T) {
	h := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	h.start()
	main := h.m.mainPID
	h.request(evStop)
	if got := h.sentTo(main); len(got) == 0 || got[0] != sigTERM {
		t.Fatalf("KillMode=process: %v", got)
	}
	n := newHarness(t, Config{KillMode: coresvc.KillModeNone, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	n.start()
	main = n.m.mainPID
	stop := n.request(evStop)
	n.wantReply(stop, 0)
	n.want(coresvc.StateInactive, coresvc.SubDead)
	if len(n.sentTo(main)) != 0 || !slices.Contains(n.forgotten, main) {
		t.Fatalf("KillMode=none: signals %v, forgotten %v", n.sentTo(main), n.forgotten)
	}
}

// TestStopEscalation: KillSignal=, then SIGCONT, then SIGHUP on SendSIGHUP=;
// FinalKillSignal= after TimeoutStopSec=; SendSIGKILL=no skips it.
func TestStopEscalation(t *testing.T) {
	quit := coreproc.Signal(syscall.SIGQUIT)
	h := newHarness(t, Config{
		KillSignal: sigINT, SendSIGHUP: true, FinalKillSignal: quit, TimeoutStop: time.Second,
		ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")},
	})
	h.start()
	main := h.m.mainPID
	h.request(evStop)
	h.advance(time.Second)
	want := []coreproc.Signal{sigINT, h.m.s.sigcont, sigHUP, quit}
	if got := h.sentTo(main); !slices.Equal(got, want) {
		t.Fatalf("signals %v, want %v", got, want)
	}
	n := newHarness(t, Config{SendSIGKILL: coresvc.ToggleNo, TimeoutStop: time.Second, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	n.start()
	main = n.m.mainPID
	n.request(evStop)
	n.advance(time.Second)
	if slices.Contains(n.sentTo(main), sigKILL) {
		t.Fatal("SendSIGKILL=no sent SIGKILL")
	}
}

// TestRestartKillSignal: a restart job stops with RestartKillSignal=, then
// starts again.
func TestRestartKillSignal(t *testing.T) {
	usr1 := coreproc.Signal(syscall.SIGUSR1)
	h := newHarness(t, Config{RestartKillSignal: usr1, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	h.start()
	main := h.m.mainPID
	id := h.request(evRestart)
	if got := h.sentTo(main); len(got) == 0 || got[0] != usr1 {
		t.Fatalf("restart signals %v, want SIGUSR1", got)
	}
	h.kill(main, usr1)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	if h.m.mainPID == main {
		t.Fatal("the restart did not spawn a new main process")
	}
}

// TestJobs: a stop cancels a start running; a start during a stop runs after
// it; a reload on a unit not up is refused.
func TestJobs(t *testing.T) {
	h := newHarness(t, Config{Type: coresvc.TypeNotify, Notify: stubNotify{}, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
	start := h.start()
	main := h.m.mainPID
	stop := h.request(evStop)
	h.wantReply(start, coresvc.CodeJobCanceled)
	again := h.start()
	h.wantWaiting(again)
	h.kill(main, sigTERM)
	h.wantReply(stop, 0)
	h.want(coresvc.StateActivating, coresvc.SubStart)
	h.notify(h.m.mainPID, "READY=1")
	h.wantReply(again, 0)
	i := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, ExecReload: []coresvc.CommandValue{cmd(t, "/bin/r")}})
	i.wantReply(i.request(evReload), coresvc.CodeReloadFailed)
}

// TestReplayIsDeterministic: the same events give the same models and the
// same effects — the reducer reads nothing but its arguments.
func TestReplayIsDeterministic(t *testing.T) {
	run := func() ([]coresvc.StateValue, []effect) {
		h := newHarness(t, Config{Restart: coresvc.RestartAlways, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}})
		h.start()
		h.exit(h.m.mainPID, 1)
		h.advance(time.Second)
		h.request(evStop)
		return h.published, h.spawns
	}
	p1, s1 := run()
	p2, s2 := run()
	if !slices.Equal(p1, p2) || len(s1) != len(s2) {
		t.Fatal("two replays of the same events differ")
	}
}

// cause drives a started simple (or notify) unit to one exit cause of Table 1.
type cause struct {
	name string
	// notify runs the unit as Type=notify with a watchdog.
	notify bool
	run    func(h *harness, main int)
}

// table1Causes are the rows of systemd.service(5)'s Table 1 the engine meets in
// S0b, each reached by events only. The OOM row needs the cgroup (S0g).
var table1Causes = []struct {
	cause
	want [7]bool // no, on-success, on-failure, on-abnormal, on-watchdog, on-abort, always
}{
	{
		cause{"clean exit code", false, func(h *harness, main int) { h.exit(main, 0) }},
		[7]bool{false, true, false, false, false, false, true},
	},
	{
		cause{"clean signal", false, func(h *harness, main int) { h.kill(main, coreproc.Signal(syscall.SIGTERM)) }},
		[7]bool{false, true, false, false, false, false, true},
	},
	{
		cause{"unclean exit code", false, func(h *harness, main int) { h.exit(main, 1) }},
		[7]bool{false, false, true, false, false, false, true},
	},
	{
		cause{"unclean signal", false, func(h *harness, main int) { h.kill(main, coreproc.Signal(syscall.SIGSEGV)) }},
		[7]bool{false, false, true, true, false, true, true},
	},
	{cause{"timeout", false, func(h *harness, main int) {
		// RuntimeMaxSec=: the stop it runs is a timeout; SIGTERM ends it.
		h.advance(30 * time.Minute)
		h.kill(main, coreproc.Signal(syscall.SIGTERM))
	}}, [7]bool{false, false, true, true, false, false, true}},
	{cause{"watchdog", true, func(h *harness, main int) {
		h.advance(30 * time.Second)
		h.kill(main, coreproc.Signal(syscall.SIGABRT))
	}}, [7]bool{false, false, true, true, true, false, true}},
	{cause{"Fail", false, func(h *harness, main int) {
		h.request(evFail)
		h.kill(main, coreproc.Signal(syscall.SIGABRT))
	}}, [7]bool{false, false, true, true, true, false, true}},
}

// TestRestartTable1Replay is ADR 0214 §10's replay: for each of Restart='s seven
// values and each exit cause, the reducer is driven from inactive by events
// only, and lands in auto-restart exactly where Table 1 has an X.
func TestRestartTable1Replay(t *testing.T) {
	for _, row := range table1Causes {
		for col, policy := range policies {
			t.Run(row.name+"/"+policy.String(), func(t *testing.T) {
				cfg := Config{
					Restart: policy, ExecStart: []coresvc.CommandValue{cmd(t, "/bin/daemon")},
					RuntimeMax: 30 * time.Minute, StartLimitInterval: coresvc.Zero,
				}
				if row.notify {
					cfg.Type, cfg.Watchdog, cfg.Notify, cfg.RuntimeMax = coresvc.TypeNotify, 10*time.Second, stubNotify{}, 0
				}
				h := newHarness(t, cfg)
				id := h.start()
				main := h.m.mainPID
				if row.notify {
					h.notify(main, "READY=1")
				}
				h.wantReply(id, 0)
				row.run(h, main)
				restarted := h.m.sub == coresvc.SubAutoRestart
				if restarted != row.want[col] {
					t.Fatalf("Restart=%s after %s: at %s (result %s), restart %v want %v",
						policy, row.name, h.m.sub, h.m.result, restarted, row.want[col])
				}
				if !restarted && h.m.state != coresvc.StateInactive && h.m.state != coresvc.StateFailed {
					t.Fatalf("not restarted but at %s", h.m.state)
				}
			})
		}
	}
}
