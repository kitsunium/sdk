package service

import (
	"context"
	"slices"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The cgroup modes (S0g, kitsunium/sdk#734) replayed: the reducer driven by
// events only, the group's population and its OOM kills scripted as the
// shell would read them.

// unitStub is a CgroupUnit the replay never calls: its directory alone is
// read, at resolve.
type unitStub struct {
	stubCgroup
	path string
}

func (u *unitStub) Path() string                         { return u.path }
func (*unitStub) Members() ([]int, error)                { return nil, nil }
func (*unitStub) Stat() (coreproc.GroupStatValue, error) { return coreproc.GroupStatValue{}, nil }
func (*unitStub) Delegate(int, int) error                { return nil }
func (*unitStub) DeleteBeneath() error                   { return nil }
func (*unitStub) SetMemoryOOMGroup(bool) error           { return nil }
func (*unitStub) Watch(context.Context) (<-chan coreproc.GroupEventsValue, error) {
	return nil, nil
}

// unitDir is unitStub's directory in these tests.
const unitDir string = "/sys/fs/cgroup/test.slice/u"

// overUnit gives cfg a CgroupUnit, and an ExecStart= when it has none.
func overUnit(t *testing.T, cfg Config) Config {
	t.Helper()
	cfg.Cgroup = &unitStub{path: unitDir}
	if len(cfg.ExecStart) == 0 {
		cfg.ExecStart = []coresvc.CommandValue{cmd(t, "/bin/d")}
	}
	return cfg
}

// killedBy is an exit by sig.
func killedBy(sig coreproc.Signal) coreproc.ExitValue {
	return coreproc.ExitValue{Code: -1, Signal: sig, Signaled: true}
}

// TestCgroupSettings: KillMode='s zero is control-group with a CgroupUnit and
// process without; OOMPolicy='s is stop, continue under Delegate=; every
// spawn goes into the unit's group unless the template names a group within
// it, and one outside is CONFIG_INVALID naming CgroupPath.
func TestCgroupSettings(t *testing.T) {
	with, err := resolve(&Config{Name: "u", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, Cgroup: &unitStub{path: unitDir}})
	if err != nil || with.killMode != coresvc.KillModeControlGroup || with.oomPolicy != coresvc.OOMPolicyStop || with.cgroupPath != unitDir {
		t.Fatalf("with a CgroupUnit: kill %s, oom %s, path %q, %v", with.killMode, with.oomPolicy, with.cgroupPath, err)
	}
	for _, cg := range []coresvc.CgroupKill{nil, stubCgroup{}, stubMembers{}} {
		without, rerr := resolve(&Config{Name: "u", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, Cgroup: cg})
		if rerr != nil || without.killMode != coresvc.KillModeProcess || without.cgroupPath != "" {
			t.Fatalf("with %T: kill %s, path %q, %v; want process", cg, without.killMode, without.cgroupPath, rerr)
		}
	}
	delegated, err := resolve(&Config{Name: "u", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, Cgroup: &unitStub{path: unitDir}, Delegate: true})
	if err != nil || delegated.oomPolicy != coresvc.OOMPolicyContinue || !delegated.on(setDelegate) {
		t.Fatalf("under Delegate=: oom %s, %v; want continue", delegated.oomPolicy, err)
	}
	beneath := Config{Name: "u", ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, Cgroup: &unitStub{path: unitDir}}
	beneath.Spec.CgroupPath = unitDir + "/service"
	s, rerr := resolve(&beneath)
	if rerr != nil {
		t.Fatalf("a group beneath: %v; want the template's kept", rerr)
	}
	if s.cgroupPath != "" {
		t.Fatalf("a group beneath: path %q; want the template's kept", s.cgroupPath)
	}
	outside := beneath
	outside.Spec.CgroupPath = "/sys/fs/cgroup/test.slice/other"
	if _, rerr := resolve(&outside); !hasCode(rerr, coresvc.CodeConfigInvalid) || field(rerr, "directive") != "CgroupPath" {
		t.Fatalf("a group outside: %v, want CONFIG_INVALID naming CgroupPath", rerr)
	}
	spec := buildSpec(&coreproc.Spec{}, with, &effect{cmd: cmd(t, "/bin/d")})
	if spec.CgroupPath != unitDir {
		t.Fatalf("a spawn's CgroupPath %q, want the unit's group", spec.CgroupPath)
	}
}

// TestKillModeControlGroup: the stop signals the whole group — KillSignal=
// then SIGCONT, the main process through the group, never twice through its
// handle —, and waits for it: the main process gone, a child left keeps the
// final step waiting until the group empties; FinalKillSignal= after
// TimeoutStopSec=, the run then failed with timeout as systemd fails it. A
// group that emptied at once is waited for by no step; an outcome for a step
// the unit left is stale.
func TestKillModeControlGroup(t *testing.T) {
	h := newHarness(t, overUnit(t, Config{TimeoutStop: 5 * time.Second}))
	h.held = 1
	h.start()
	main := h.m.mainPID
	if h.prepares != 1 {
		t.Fatalf("%d spawns prepared the group, want the start's first", h.prepares)
	}
	h.held = 2
	stop := h.request(evStop)
	h.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	if len(h.groupSignals) != 1 || len(h.signals) != 0 {
		t.Fatalf("group signals %v, process signals %v; want one group signal", h.groupSignals, h.signals)
	}
	first := h.groupSignals[0]
	if first.sub != coresvc.SubStopSigterm || !slices.Equal(first.signals, []coreproc.Signal{sigTERM, h.m.s.sigcont}) || !slices.Equal(first.pids, []int{main, 0}) {
		t.Fatalf("the first group signal %+v", first)
	}
	h.endIn(main, killedBy(sigTERM), 1, 0)
	h.want(coresvc.StateDeactivating, coresvc.SubFinalSigterm)
	h.wantWaiting(stop)
	h.advance(5 * time.Second)
	h.want(coresvc.StateDeactivating, coresvc.SubFinalSigkill)
	last := h.groupSignals[len(h.groupSignals)-1]
	if last.sub != coresvc.SubFinalSigkill || !slices.Equal(last.signals, []coreproc.Signal{sigKILL}) {
		t.Fatalf("the final group signal %+v, want SIGKILL", last)
	}
	h.group(0, false)
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	h.wantReply(stop, 0)
	if h.m.result != coresvc.ResultTimeout {
		t.Fatalf("result %s, want timeout", h.m.result)
	}

	quick := newHarness(t, overUnit(t, Config{}))
	quick.held = 1
	quick.start()
	stop = quick.request(evStop)
	quick.endIn(quick.m.mainPID, killedBy(sigTERM), 0, 0)
	quick.want(coresvc.StateInactive, coresvc.SubDead)
	quick.wantReply(stop, 0)
	if n := len(quick.groupSignals); n != 3 {
		t.Fatalf("%d group signals, want the terminate, the final terminate and the final kill, each on an empty group", n)
	}
}

// TestKillModeMixed: the terminate reaches the main process alone, through
// its handle; once it is gone the final kill reaches the whole group, and the
// stop waits for it to be empty.
func TestKillModeMixed(t *testing.T) {
	h := newHarness(t, overUnit(t, Config{KillMode: coresvc.KillModeMixed}))
	h.held = 1
	h.start()
	main := h.m.mainPID
	h.held = 2
	stop := h.request(evStop)
	if got := h.sentTo(main); !slices.Equal(got, []coreproc.Signal{sigTERM, h.m.s.sigcont}) || len(h.groupSignals) != 0 {
		t.Fatalf("mixed's terminate: main %v, group %v", got, h.groupSignals)
	}
	h.endIn(main, killedBy(sigTERM), 1, 0)
	h.want(coresvc.StateDeactivating, coresvc.SubFinalSigkill)
	if len(h.groupSignals) != 1 || h.groupSignals[0].sub != coresvc.SubFinalSigkill || !slices.Equal(h.groupSignals[0].signals, []coreproc.Signal{sigKILL}) {
		t.Fatalf("mixed's kill %+v, want the group's SIGKILL", h.groupSignals)
	}
	h.wantWaiting(stop)
	h.group(0, false)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	h.wantReply(stop, 0)
}

// TestKillModeProcessAndNoneLeaveTheGroup: with a CgroupUnit, an explicit
// process signals the main process alone and the stop ends with it, a child
// left in the group; none signals nothing.
func TestKillModeProcessAndNoneLeaveTheGroup(t *testing.T) {
	h := newHarness(t, overUnit(t, Config{KillMode: coresvc.KillModeProcess}))
	h.held = 1
	h.start()
	main := h.m.mainPID
	stop := h.request(evStop)
	h.endIn(main, killedBy(sigTERM), 1, 0)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	h.wantReply(stop, 0)
	if len(h.groupSignals) != 0 || !slices.Equal(h.sentTo(main), []coreproc.Signal{sigTERM, h.m.s.sigcont}) {
		t.Fatalf("process: group %v, main %v", h.groupSignals, h.sentTo(main))
	}
	n := newHarness(t, overUnit(t, Config{KillMode: coresvc.KillModeNone}))
	n.held = 1
	n.start()
	stop = n.request(evStop)
	n.wantReply(stop, 0)
	if len(n.groupSignals) != 0 || len(n.signals) != 0 {
		t.Fatalf("none: group %v, signals %v", n.groupSignals, n.signals)
	}
}

// TestExitTypeCgroup: the main process gone, a child left keeps the unit up —
// not before the last process leaves —; the group's emptying stops it,
// cleanly. Under ExitType=main the same end stops it at once, signalling the
// child. A main process that failed fails the unit, whatever is left.
func TestExitTypeCgroup(t *testing.T) {
	h := newHarness(t, overUnit(t, Config{ExitType: coresvc.ExitTypeCgroup}))
	h.held = 1
	h.wantReply(h.start(), 0)
	h.endIn(h.m.mainPID, coreproc.ExitValue{}, 1, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.group(1, false)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.group(0, false)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	if h.m.result != coresvc.ResultSuccess {
		t.Fatalf("result %s, want success", h.m.result)
	}
	m := newHarness(t, overUnit(t, Config{}))
	m.held = 1
	m.start()
	m.endIn(m.m.mainPID, coreproc.ExitValue{}, 1, 0)
	m.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	f := newHarness(t, overUnit(t, Config{ExitType: coresvc.ExitTypeCgroup}))
	f.held = 1
	f.start()
	f.endIn(f.m.mainPID, coreproc.ExitValue{Code: 3}, 1, 0)
	f.want(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	f.group(0, false)
	f.want(coresvc.StateFailed, coresvc.SubFailed)
	if f.m.result != coresvc.ResultExitCode {
		t.Fatalf("result %s, want exit-code", f.m.result)
	}
}

// TestForkingUpOnItsCgroupEndsWhenEmpty: a forking unit whose main process
// stayed unknown is up while its cgroup has members; the cgroup's emptying,
// which S0c left to a stop, ends it.
func TestForkingUpOnItsCgroupEndsWhenEmpty(t *testing.T) {
	cfg := overUnit(t, Config{Type: coresvc.TypeForking, Attacher: stubAttacher{}})
	h := newHarness(t, cfg)
	h.adoptions = []adoption{{flags: flagPopulated}}
	h.held = 1
	id := h.start()
	h.exit(h.m.controlPID, 0)
	h.wantReply(id, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	if h.m.mainPID != 0 || !h.m.has(flagCgroupGood) {
		t.Fatalf("main %d, cgroup good %v: want up on the cgroup alone", h.m.mainPID, h.m.has(flagCgroupGood))
	}
	h.group(2, false)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.group(0, false)
	h.want(coresvc.StateInactive, coresvc.SubDead)
}

// TestCgroupEmptiedWhileStarting: a notify unit whose main process ended
// before READY=1, waiting for another process to send it, fails with
// protocol once nobody is left; a forking unit waiting for a PID file
// nobody is left to write gives it up at once.
func TestCgroupEmptiedWhileStarting(t *testing.T) {
	h := newHarness(t, overUnit(t, Config{
		Type: coresvc.TypeNotify, Notify: stubNotify{}, NotifyAccess: coresvc.NotifyAccessAll, RemainAfterExit: true,
	}))
	h.held = 1
	id := h.start()
	h.endIn(h.m.mainPID, coreproc.ExitValue{}, 1, 0)
	h.want(coresvc.StateActivating, coresvc.SubStart)
	h.group(0, false)
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	h.wantReply(id, coresvc.CodeStartFailed)
	if h.m.result != coresvc.ResultProtocol {
		t.Fatalf("result %s, want protocol", h.m.result)
	}
	p := newHarness(t, overUnit(t, Config{Type: coresvc.TypeForking, Attacher: stubAttacher{}, PIDFile: "/run/d.pid"}))
	p.adoptions = []adoption{{reason: "PID_FILE_REFUSED", flags: flagPopulated}}
	p.held = 1
	id = p.start()
	p.exit(p.m.controlPID, 0)
	p.want(coresvc.StateActivating, coresvc.SubStart)
	p.group(0, false)
	p.want(coresvc.StateFailed, coresvc.SubFailed)
	p.wantReply(id, coresvc.CodeStartFailed)
	if p.m.result != coresvc.ResultProtocol || p.fileReads != 1 {
		t.Fatalf("result %s after %d reads, want protocol after one", p.m.result, p.fileReads)
	}
}

// TestOOMPolicy: an OOM kill in the unit's group — a worker's, the main
// process running on — keeps the unit under continue; stop stops it, kill
// sends the final kill at once, both ending it with oom-kill, which
// Restart=on-failure restarts. The default is stop, continue under
// Delegate=. A main process the OOM killer ended is read as oom-kill, not
// as a signal: the kill is taken before the end.
func TestOOMPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   coresvc.OOMPolicy
		delegate bool
		stops    bool
		kill     bool
	}{
		{"continue", coresvc.OOMPolicyContinue, false, false, false},
		{"stop", coresvc.OOMPolicyStop, false, true, false},
		{"kill", coresvc.OOMPolicyKill, false, true, true},
		{"default", coresvc.OOMPolicyDefault, false, true, false},
		{"default under Delegate=", coresvc.OOMPolicyDefault, true, false, false},
	} {
		h := newHarness(t, overUnit(t, Config{OOMPolicy: tc.policy, Delegate: tc.delegate}))
		h.held = 2
		h.start()
		main := h.m.mainPID
		h.group(2, true)
		if !tc.stops {
			h.want(coresvc.StateActive, coresvc.SubRunning)
			if h.m.result != coresvc.ResultSuccess {
				t.Errorf("%s: result %s, want the unit untouched", tc.name, h.m.result)
			}
			continue
		}
		wantSub := coresvc.SubStopSigterm
		if tc.kill {
			wantSub = coresvc.SubFinalSigkill
		}
		h.want(coresvc.StateDeactivating, wantSub)
		if last := h.groupSignals[len(h.groupSignals)-1]; last.sub != wantSub {
			t.Errorf("%s: group signal for %s, want %s", tc.name, last.sub, wantSub)
		}
		h.endIn(main, killedBy(sigKILL), 0, 0)
		h.group(0, false)
		h.want(coresvc.StateFailed, coresvc.SubFailed)
		if h.m.result != coresvc.ResultOOMKill {
			t.Errorf("%s: result %s, want oom-kill", tc.name, h.m.result)
		}
	}
	r := newHarness(t, overUnit(t, Config{Restart: coresvc.RestartOnFailure}))
	r.held = 1
	r.start()
	r.endIn(r.m.mainPID, killedBy(sigKILL), 0, flagOOM)
	r.want(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	if r.m.result != coresvc.ResultOOMKill {
		t.Fatalf("the main process's OOM kill: result %s, want oom-kill", r.m.result)
	}
}

// TestDelegateReplay: every start's first spawn prepares the unit's group —
// the watch, the delegation —, and Delegate= removes what the service made
// beneath it at every end of a run; without Delegate= nothing is removed.
func TestDelegateReplay(t *testing.T) {
	h := newHarness(t, overUnit(t, Config{Delegate: true, ExecStartPre: []coresvc.CommandValue{cmd(t, "/bin/pre")}}))
	h.start()
	h.exit(h.m.controlPID, 0)
	if h.prepares != 1 || !h.spawns[0].has(optPrepare) || h.spawns[1].has(optPrepare) {
		t.Fatalf("prepares %d (first %v, second %v), want the start's first spawn alone", h.prepares, h.spawns[0].has(optPrepare), h.spawns[1].has(optPrepare))
	}
	h.stopAndExit()
	h.want(coresvc.StateInactive, coresvc.SubDead)
	if h.prunes != 1 {
		t.Fatalf("%d cleanups after a run, want one", h.prunes)
	}
	h.start()
	if h.prepares != 2 {
		t.Fatalf("the second start prepared %d times in all, want 2", h.prepares)
	}
	n := newHarness(t, overUnit(t, Config{}))
	n.start()
	n.stopAndExit()
	if n.prunes != 0 || n.prepares != 1 {
		t.Fatalf("without Delegate=: %d cleanups, %d prepares", n.prunes, n.prepares)
	}
	plain := newHarness(t, Config{ExecStart: []coresvc.CommandValue{cmd(t, "/bin/d")}, Cgroup: stubCgroup{}})
	plain.start()
	if plain.prepares != 0 {
		t.Fatalf("a CgroupKill alone prepared %d times", plain.prepares)
	}
}
