package service

import (
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// Type=forking (S0c, kitsunium/sdk#735) is service.c's SERVICE_FORKING: the
// ExecStart= command is a control process, and once it exited cleanly the
// daemon it left is the main process — the PID its PID file names, read by
// the Attacher alone (service_load_pid_file, through S3's AttachPIDFile), or
// the one the cgroup's members point at (service_search_main_pid). The
// reducer asks, the shell reads, and the answer comes back as evAttached.

// pidFileRetry is how long a forking unit waits before it reads a PID file it
// could not read again — systemd's inotify watch, on the unit's clock.
const pidFileRetry time.Duration = 100 * time.Millisecond

// forkingStarted takes the end of a Type=forking unit's ExecStart= command:
// a failure fails the start; a clean end looks for the daemon.
func (st *step) forkingStarted(f coresvc.Result) {
	//: the command failed: what it left is signalled.
	if f != coresvc.ResultSuccess {
		st.enterSignal(coresvc.SubStopSigterm, f)
		return
	}
	//: MAINPID= named the daemon already.
	if st.m.mainPID != 0 {
		st.enterStartPost()
		return
	}
	st.findMain()
}

// startPostDone goes on once ExecStartPost= succeeded: up — or, for a
// forking unit whose daemon is not found yet, after one more look.
func (st *step) startPostDone() {
	m := st.m
	//: a forking unit with no main process yet: its PID file may be
	//: ExecStartPost='s.
	if m.s.typ == coresvc.TypeForking && m.mainPID == 0 {
		st.findMain()
		return
	}
	st.enterRunning(coresvc.ResultSuccess)
}

// findMain asks the shell for the daemon: the PID file, or the cgroup's
// members — a guess, or only whether any is left (GuessMainPID=no).
func (st *step) findMain() {
	s := st.m.s
	//: PIDFile= names it.
	if s.pidFile != "" {
		st.emit(&effect{kind: efAttachFile})
		return
	}
	st.emit(&effect{kind: efGuess, opts: optIf(optGuess, s.on(setGuess))})
}

// waitingForMain reports a forking unit that looks for its daemon: no main
// process, no command running, at the start or after its post-start.
func (st *step) waitingForMain() bool {
	m := st.m
	//: forking, nothing running, at a start step.
	return m.s.typ == coresvc.TypeForking && m.mainPID == 0 && m.controlPID == 0 &&
		(m.sub == coresvc.SubStart || m.sub == coresvc.SubStartPost)
}

// pidFileRead takes the outcome of a PID file's read: the daemon adopted, or
// — while it cannot be read — the next read armed, until TimeoutStartSec=
// ends the wait, unless ExecStartPost= may still write it or the cgroup says
// nothing is left to write it.
func (st *step) pidFileRead() {
	m := st.m
	ev := st.ev
	//: the unit no longer waits for it.
	if !st.waitingForMain() {
		st.dropAdopted()
		return
	}
	//: the daemon.
	if ev.has(flagOK) {
		st.adoptMain(ev.pid)
		st.mainFound()
		return
	}
	m.pidFileCause = ev.reason
	//: ExecStartPost= may write it: read again after it.
	if m.sub == coresvc.SubStart && len(m.s.lists[listStartPost]) > 0 {
		st.enterStartPost()
		return
	}
	//: the cgroup is empty: nothing is left to write it.
	if ev.has(flagEmpty) {
		st.mainNeverFound()
		return
	}
	st.arm(timerPIDFile, pidFileRetry)
}

// pidFileDue reads the PID file again, if the unit still waits for it.
func (st *step) pidFileDue() {
	//: still waiting.
	if st.waitingForMain() {
		st.emit(&effect{kind: efAttachFile})
	}
}

// mainGuessed takes the outcome of the cgroup's guess: the daemon adopted, or
// none found — the unit then up while its cgroup has members, as systemd's
// service_good falls back on cgroup_good.
func (st *step) mainGuessed() {
	m := st.m
	ev := st.ev
	//: the unit no longer waits for it.
	if !st.waitingForMain() {
		st.dropAdopted()
		return
	}
	//: the daemon, or the cgroup's population.
	if ev.has(flagOK) {
		st.adoptMain(ev.pid)
	} else {
		m.set(flagCgroupGood, ev.has(flagPopulated))
	}
	st.mainFound()
}

// dropAdopted leaves a process an outcome adopted that nobody waits for any
// more.
func (st *step) dropAdopted() {
	//: adopted.
	if st.ev.has(flagOK) {
		st.emit(&effect{kind: efForget, pid: st.ev.pid})
	}
}

// adoptMain makes pid the unit's main process; a former one is left running,
// no longer the unit's.
func (st *step) adoptMain(pid int) {
	m := st.m
	//: a former main process.
	if m.mainPID != 0 && m.mainPID != pid {
		st.emit(&effect{kind: efForget, pid: m.mainPID})
	}
	m.mainPID = pid
	m.set(flagMainExited, false)
}

// mainFound goes on once the daemon is known, or known to be unknowable:
// ExecStartPost= after the start, up after the post-start.
func (st *step) mainFound() {
	//: from the start step.
	if st.m.sub == coresvc.SubStart {
		st.enterStartPost()
		return
	}
	st.enterRunning(coresvc.ResultSuccess)
}

// mainNeverFound fails a start whose PID file nothing is left to write: the
// result protocol, as service.c fails a daemon that never wrote it.
func (st *step) mainNeverFound() {
	//: from the start step, what runs is signalled.
	if st.m.sub == coresvc.SubStart {
		st.enterSignal(coresvc.SubStopSigterm, coresvc.ResultProtocol)
		return
	}
	st.enterStop(coresvc.ResultProtocol)
}
