//go:build !windows

package service

import (
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// The replay of Type=forking (S0c, kitsunium/sdk#735): the ExecStart= command
// is a control process, the daemon it leaves is adopted from PIDFile= or
// guessed among the cgroup's members, and its end without a status is clean.

// forking is a Type=forking configuration reading /run/d.pid.
func forking(t *testing.T) Config {
	return Config{
		Type: coresvc.TypeForking, PIDFile: "/run/d.pid", Attacher: stubAttacher{},
		ExecStart: []coresvc.CommandValue{cmd(t, "/usr/sbin/d")},
	}
}

// goneUnknown ends an adopted process: gone, its status unknowable.
func (h *harness) goneUnknown(pid int) {
	h.feed(event{kind: evExit, pid: pid, exit: coreproc.ExitValue{Code: -1}, flags: flagUnknown})
}

// TestForkingPIDFile: the command is a control process; once it exited
// cleanly the PID file's daemon is the main process and the unit is up; the
// daemon gone without a status is a clean end, so Restart= follows the
// oracle's success row — always and on-success restart, the others do not.
func TestForkingPIDFile(t *testing.T) {
	h := newHarness(t, forking(t))
	h.adoptions = []adoption{{pid: 4711}}
	id := h.start()
	h.want(coresvc.StateActivating, coresvc.SubStart)
	starter := h.m.controlPID
	if starter == 0 || h.m.mainPID != 0 || h.lastSpawn().main {
		t.Fatalf("the forking command: control %d, main %d, spawned as main %v", starter, h.m.mainPID, h.lastSpawn().main)
	}
	if h.fileReads != 0 {
		t.Fatal("the PID file was read before the command ended")
	}
	h.wantWaiting(id)
	h.exit(starter, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	if h.m.mainPID != 4711 || h.fileReads != 1 {
		t.Fatalf("main %d after %d reads, want 4711 after one", h.m.mainPID, h.fileReads)
	}
	h.goneUnknown(4711)
	h.want(coresvc.StateInactive, coresvc.SubDead)
	if h.m.result != coresvc.ResultSuccess {
		t.Fatalf("the daemon gone: result %s, want success", h.m.result)
	}
	for policy, restarts := range map[coresvc.Restart]bool{
		coresvc.RestartAlways: true, coresvc.RestartOnSuccess: true, coresvc.RestartNo: false,
		coresvc.RestartOnFailure: false, coresvc.RestartOnAbnormal: false, coresvc.RestartOnAbort: false,
	} {
		cfg := forking(t)
		cfg.Restart = policy
		r := newHarness(t, cfg)
		r.adoptions = []adoption{{pid: 4711}}
		r.start()
		r.exit(r.m.controlPID, 0)
		r.goneUnknown(4711)
		if got := r.m.state == coresvc.StateAutoRestart; got != restarts {
			t.Errorf("Restart=%s: the daemon gone leaves %s/%s, want a restart %v", policy, r.m.state, r.m.sub, restarts)
		}
	}
}

// TestForkingPIDFileRetriedUntilTimeout: a PID file that cannot be read is
// read again every 100 ms; TimeoutStartSec= ends the wait with the result
// timeout and START_FAILED naming the last refusal; one read that succeeds
// before it brings the unit up.
func TestForkingPIDFileRetriedUntilTimeout(t *testing.T) {
	cfg := forking(t)
	cfg.TimeoutStart = 2 * time.Second
	h := newHarness(t, cfg)
	id := h.start()
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActivating, coresvc.SubStart)
	if d := h.deadline(timerPIDFile); d != pidFileRetry {
		t.Fatalf("the next read in %s, want %s", d, pidFileRetry)
	}
	h.advance(pidFileRetry)
	if h.fileReads != 2 {
		t.Fatalf("%d reads after one retry, want 2", h.fileReads)
	}
	h.advance(2 * time.Second)
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	if h.m.result != coresvc.ResultTimeout {
		t.Fatalf("result %s, want timeout", h.m.result)
	}
	h.wantReply(id, coresvc.CodeStartFailed)
	if got := field(h.replies[id], "pidfile"); got != "PID_FILE_REFUSED" {
		t.Fatalf("START_FAILED names the PID file's refusal %q", got)
	}
	if h.timers[timerPIDFile].on {
		t.Fatal("the PID file is still read after the start failed")
	}
	reads := h.fileReads
	h.advance(time.Second)
	if h.fileReads != reads {
		t.Fatal("a failed unit read its PID file")
	}

	late := newHarness(t, cfg)
	late.adoptions = []adoption{{reason: "PID_FILE_REFUSED"}, {reason: "PID_FILE_REFUSED"}, {pid: 4711}}
	lid := late.start()
	late.exit(late.m.controlPID, 0)
	late.advance(2 * pidFileRetry)
	late.want(coresvc.StateActive, coresvc.SubRunning)
	late.wantReply(lid, 0)
	if late.m.mainPID != 4711 {
		t.Fatalf("main %d, want 4711", late.m.mainPID)
	}
}

// TestForkingCommandFails: a command that fails fails the start; its PID
// file is never read.
func TestForkingCommandFails(t *testing.T) {
	h := newHarness(t, forking(t))
	id := h.start()
	h.exit(h.m.controlPID, 1)
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	h.wantReply(id, coresvc.CodeStartFailed)
	if h.m.result != coresvc.ResultExitCode || h.fileReads != 0 {
		t.Fatalf("result %s after %d reads, want exit-code and none", h.m.result, h.fileReads)
	}
}

// TestForkingPIDFileFromStartPost: a PID file not there once the command
// ended may be ExecStartPost='s: it is read again after it, and the unit is up
// once it is.
func TestForkingPIDFileFromStartPost(t *testing.T) {
	cfg := forking(t)
	cfg.ExecStartPost = []coresvc.CommandValue{cmd(t, "/bin/post")}
	h := newHarness(t, cfg)
	h.adoptions = []adoption{{reason: "PID_FILE_REFUSED"}, {pid: 4711}}
	id := h.start()
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActivating, coresvc.SubStartPost)
	if h.timers[timerPIDFile].on {
		t.Fatal("a retry armed while ExecStartPost= may still write the file")
	}
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	h.wantReply(id, 0)
	if h.m.mainPID != 4711 || h.fileReads != 2 {
		t.Fatalf("main %d after %d reads", h.m.mainPID, h.fileReads)
	}
}

// TestForkingEmptyCgroupIsProtocol: a PID file not there while the cgroup is
// empty will never be written: the result protocol at once, no retry.
func TestForkingEmptyCgroupIsProtocol(t *testing.T) {
	cfg := forking(t)
	cfg.Cgroup = stubMembers{}
	h := newHarness(t, cfg)
	h.adoptions = []adoption{{reason: "PID_FILE_REFUSED", flags: flagEmpty}}
	id := h.start()
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateFailed, coresvc.SubFailed)
	h.wantReply(id, coresvc.CodeStartFailed)
	if h.m.result != coresvc.ResultProtocol || h.fileReads != 1 {
		t.Fatalf("result %s after %d reads, want protocol after one", h.m.result, h.fileReads)
	}
}

// TestForkingGuess: without PIDFile= the cgroup's members are asked — the one
// guessed is the main process; none guessed leaves the unit up while the
// cgroup has members, and stops it when it has none; GuessMainPID=no asks
// for the population only.
func TestForkingGuess(t *testing.T) {
	base := Config{
		Type: coresvc.TypeForking, Attacher: stubAttacher{}, Cgroup: stubMembers{},
		ExecStart: []coresvc.CommandValue{cmd(t, "/usr/sbin/d")},
	}
	for _, tc := range []struct {
		name  string
		got   adoption
		state coresvc.State
		main  int
	}{
		{"guessed", adoption{pid: 4711, flags: flagPopulated}, coresvc.StateActive, 4711},
		{"ambiguous", adoption{flags: flagPopulated}, coresvc.StateActive, 0},
		{"empty", adoption{flags: flagEmpty}, coresvc.StateInactive, 0},
	} {
		h := newHarness(t, base)
		h.adoptions = []adoption{tc.got}
		id := h.start()
		h.exit(h.m.controlPID, 0)
		h.wantReply(id, 0)
		if h.m.state != tc.state || h.m.mainPID != tc.main || h.guesses != 1 || !h.picks[0] {
			t.Errorf("%s: %s main %d after %d guesses (pick %v), want %s main %d",
				tc.name, h.m.state, h.m.mainPID, h.guesses, h.picks, tc.state, tc.main)
		}
		if tc.state == coresvc.StateActive && tc.main == 0 {
			stop := h.request(evStop)
			h.want(coresvc.StateInactive, coresvc.SubDead)
			h.wantReply(stop, 0)
		}
	}
	no := base
	no.GuessMainPID = coresvc.ToggleNo
	h := newHarness(t, no)
	h.adoptions = []adoption{{flags: flagPopulated}}
	h.start()
	h.exit(h.m.controlPID, 0)
	h.want(coresvc.StateActive, coresvc.SubRunning)
	if len(h.picks) != 1 || h.picks[0] {
		t.Fatalf("GuessMainPID=no asked for a pick: %v", h.picks)
	}
}

// TestForkingSettings: Type=forking needs an Attacher, an absolute clean
// PIDFile= or a cgroup that lists its members; every other type ignores
// PIDFile= and GuessMainPID=, as systemd does; the forking command gets the
// main process's $NOTIFY_SOCKET, which its daemon inherits.
func TestForkingSettings(t *testing.T) {
	start := []coresvc.CommandValue{cmd(t, "/usr/sbin/d")}
	for _, tc := range []struct {
		cfg       Config
		code      errs.Code
		directive string
	}{
		{Config{Type: coresvc.TypeForking, PIDFile: "/run/d.pid"}, coresvc.CodeConfigInvalid, "Attacher"},
		{Config{Type: coresvc.TypeForking, Attacher: stubAttacher{}, PIDFile: "run/d.pid"}, coresvc.CodeConfigInvalid, "PIDFile"},
		{Config{Type: coresvc.TypeForking, Attacher: stubAttacher{}, PIDFile: "/run/../d.pid"}, coresvc.CodeConfigInvalid, "PIDFile"},
		{Config{Type: coresvc.TypeForking, Attacher: stubAttacher{}}, coresvc.CodeDirectiveUnsupported, "GuessMainPID"},
		{Config{Type: coresvc.TypeForking, Attacher: stubAttacher{}, Cgroup: stubCgroup{}}, coresvc.CodeDirectiveUnsupported, "GuessMainPID"},
		{Config{GuessMainPID: coresvc.Toggle(9)}, coresvc.CodeConfigInvalid, "GuessMainPID"},
	} {
		cfg := tc.cfg
		cfg.Name, cfg.ExecStart = "u", start
		_, err := resolve(&cfg)
		if !hasCode(err, tc.code) || field(err, "directive") != tc.directive {
			t.Errorf("%+v: %v, want code %x naming %s", tc.cfg, err, tc.code, tc.directive)
		}
	}
	s, err := resolve(&Config{Name: "u", PIDFile: "/run/d.pid", GuessMainPID: coresvc.ToggleNo, ExecStart: start})
	if err != nil || s.pidFile != "" || s.on(setGuess) {
		t.Fatalf("Type=simple with PIDFile= and GuessMainPID=: %v, pidFile %q", err, s.pidFile)
	}
	cfg := forking(t)
	cfg.Notify, cfg.NotifyAccess = stubNotify{}, coresvc.NotifyAccessMain
	h := newHarness(t, cfg)
	h.start()
	if !hasVar(h.lastSpawn(), "NOTIFY_SOCKET=@test-notify") {
		t.Fatalf("the forking command without $NOTIFY_SOCKET: %v", h.lastSpawn().vars)
	}
}
