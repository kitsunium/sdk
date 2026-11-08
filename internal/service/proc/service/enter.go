package service

import (
	"strconv"
	"strings"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The enter* methods are service.c's service_enter_*: each moves the unit to a
// step and asks for what that step does. They fold the outcome f into the
// unit's result as service.c does — the first failure wins.

// setState moves the unit, stamping when its state changed and stopping the
// watchdog outside the steps it runs in.
func (st *step) setState(state coresvc.State, sub coresvc.Sub) {
	m := st.m
	//: a new state is stamped.
	if m.state != state {
		m.changed = st.ev.at
	}
	//: a forking unit reads its PID file again only at the step it waits in.
	if m.sub != sub {
		st.disarm(timerPIDFile)
	}
	m.state, m.sub = state, sub
	//: the watchdog runs from the start-up's end to the stop's.
	if !watchdogRuns(sub) {
		st.disarm(timerWatchdog)
	}
}

// fold keeps the first failure as the unit's result.
func (st *step) fold(f coresvc.Result) {
	//: the first failure wins.
	if st.m.result == coresvc.ResultSuccess {
		st.m.result = f
	}
}

// arm arms timer k to fire after d; Infinity disarms it.
func (st *step) arm(k timerKind, d time.Duration) {
	//: no timeout.
	if d == coresvc.Infinity {
		st.disarm(k)
		return
	}
	st.armAt(k, st.ev.at.Add(d))
}

// armAt arms timer k for at.
func (st *step) armAt(k timerKind, at time.Time) {
	m := st.m
	m.seq[k]++
	m.deadlines[k] = at
	st.emit(&effect{kind: efArm, timer: k, at: at, seq: m.seq[k]})
}

// disarm disarms timer k; a fire of its last arming is stale from now on.
func (st *step) disarm(k timerKind) {
	m := st.m
	//: armed.
	if m.deadlines[k].IsZero() {
		return
	}
	m.seq[k]++
	m.deadlines[k] = time.Time{}
	st.emit(&effect{kind: efDisarm, timer: k, seq: m.seq[k]})
}

// spawn asks for the index-th command of l, as the main process or a control
// one. A Type=forking unit's ExecStart= is a control process given the main
// process's variables: the daemon it forks inherits them.
func (st *step) spawn(l list, index int, main bool) {
	m := st.m
	m.list, m.cmd = l, index
	cmd := m.s.lists[l][index]
	//: the main command's prefixes say how its end reads.
	if main {
		m.mainFlags = cmd.Flags
	}
	asMain := main || (l == listStart && m.s.typ == coresvc.TypeForking)
	ef := effect{kind: efSpawn, list: l, cmd: cmd, main: main, vars: st.vars(l, asMain), opts: optIf(optPrepare, m.has(flagPrepare))}
	m.set(flagPrepare, false)
	//: the store goes to ExecStart= alone, never to a command (S0d).
	if l == listStart {
		ef.entries = st.handOver()
	}
	st.emit(&ef)
}

// vars are the variables the engine sets for a command (systemd.exec(5),
// "Environment Variables Set or Propagated by the Service Manager").
func (st *step) vars(l list, main bool) []string {
	m := st.m
	v := []string{"MANAGERPID=" + strconv.Itoa(m.s.selfPID)}
	v = append(v, st.notifyVars(main)...)
	//: the store's size, for every process of a unit that has one.
	if m.s.storeMax > 0 {
		v = append(v, "FDSTORE="+strconv.Itoa(m.s.storeMax))
	}
	//: the main process, for a command.
	if !main && m.mainPID != 0 {
		v = append(v, "MAINPID="+strconv.Itoa(m.mainPID))
	}
	//: the outcome, for the stop commands.
	if l == listStop || l == listStopPost {
		v = append(v, st.outcomeVars()...)
	}
	//: a RestartMode=debug restart.
	if m.has(flagDebug) {
		v = append(v, "DEBUG_INVOCATION=1")
	}
	//: the variables.
	return v
}

// notifyVars are $NOTIFY_SOCKET, for whoever may be heard on it, and
// $WATCHDOG_USEC, for the main process.
func (st *step) notifyVars(main bool) []string {
	m := st.m
	var v []string
	heard := m.notifyAccess == coresvc.NotifyAccessExec || m.notifyAccess == coresvc.NotifyAccessAll ||
		(main && m.notifyAccess == coresvc.NotifyAccessMain)
	//: a socket, and a process that may be heard on it.
	if m.s.socket != "" && heard {
		v = append(v, "NOTIFY_SOCKET="+m.s.socket)
	}
	//: the keep-alive period, for the main process.
	if period := st.watchdogPeriod(); main && period > 0 {
		v = append(v, "WATCHDOG_USEC="+strconv.FormatInt(period.Microseconds(), 10))
	}
	//: the variables.
	return v
}

// outcomeVars are $SERVICE_RESULT, and $EXIT_CODE and $EXIT_STATUS once the
// main process's end is known.
func (st *step) outcomeVars() []string {
	m := st.m
	v := []string{"SERVICE_RESULT=" + m.result.String()}
	//: the main process's end, once known.
	if m.has(flagMainExited) {
		code, status := exitVars(m)
		v = append(v, "EXIT_CODE="+code, "EXIT_STATUS="+status)
	}
	//: the variables.
	return v
}

// exitVars are $EXIT_CODE and $EXIT_STATUS: exited and the code, or killed and
// the signal's name without SIG.
func exitVars(m *model) (code, status string) {
	//: a signal ended it.
	if m.mainExit.Signaled {
		//: killed, by name.
		return "killed", strings.TrimPrefix(m.mainExit.Signal.String(), "SIG")
	}
	//: an exit code.
	return "exited", strconv.Itoa(m.mainExit.Code)
}

// signal asks for sig to pid.
func (st *step) signal(pid int, sig coreproc.Signal) {
	//: no process, or no signal.
	if pid == 0 || sig == 0 {
		return
	}
	st.emit(&effect{kind: efSignal, pid: pid, sig: sig})
}

// forgetAll drops the main and control processes, leaving them running.
func (st *step) forgetAll() {
	m := st.m
	//: each process the unit runs.
	for _, pid := range []int{m.mainPID, m.controlPID} {
		//: a process.
		if pid != 0 {
			st.emit(&effect{kind: efForget, pid: pid})
		}
	}
	m.mainPID, m.controlPID = 0, 0
}

// beginStart is service.c's service_start: the limiter, the reset, then the
// condition. manual is a start a caller asked for, which flushes the restart
// counter.
func (st *step) beginStart(manual bool) {
	m := st.m
	st.disarm(timerState)
	m.set(flagRan, true)
	//: past the burst the start is refused, and nothing restarts it.
	if !st.limitBelow() {
		m.result = coresvc.ResultStartLimitHit
		st.enterDead(coresvc.ResultStartLimitHit, false)
		return
	}
	m.result, m.reloadResult = coresvc.ResultSuccess, coresvc.ResultSuccess
	m.set(flagMainExited, false)
	m.mainExit, m.notify = coreproc.ExitValue{}, notifyUnknown
	m.status, m.busError, m.varlinkError, m.exitStatusText, m.errno = "", "", "", "", 0
	m.notifyAccess, m.watchdogPeriod, m.pidFileCause = m.s.notifyAccess, 0, ""
	m.sandboxCause, m.sandboxFields = 0, nil
	m.set(flagForbidRestart, false)
	m.set(flagCgroupGood, false)
	m.set(flagPrepare, m.s.on(setGroupUnit) || m.s.typ == coresvc.TypeDbus)
	m.runtimeAt, m.revalidateBegin = time.Time{}, time.Time{}
	//: a caller's start flushes the restart counter.
	if manual {
		m.restarts = 0
	}
	m.set(flagDebug, !manual && m.s.restartMode == coresvc.RestartModeDebug)
	st.enterCondition()
}

// limitBelow is systemd's ratelimit_below over StartLimitIntervalSec= and
// StartLimitBurst=: every start counts, in a window that opens at the first.
func (st *step) limitBelow() bool {
	m := st.m
	s := m.s
	//: StartLimitIntervalSec=0.
	if s.on(setLimitOff) {
		//: no limit.
		return true
	}
	now := st.ev.at
	//: no window yet, or the last one is over.
	if m.limitBegin.IsZero() || (s.limitInterval != coresvc.Infinity && now.Sub(m.limitBegin) > s.limitInterval) {
		m.limitBegin, m.limitCount = now, 1
		//: the first start of a window.
		return true
	}
	//: counted, held below overflow.
	if m.limitCount <= s.limitBurst {
		m.limitCount++
	}
	//: within the burst.
	return m.limitCount <= s.limitBurst
}

// enterCondition runs ExecCondition=, or goes on.
func (st *step) enterCondition() {
	//: no condition.
	if len(st.m.s.lists[listCondition]) == 0 {
		st.enterStartPre()
		return
	}
	st.setState(coresvc.StateActivating, coresvc.SubCondition)
	st.arm(timerState, st.m.s.timeoutStart)
	st.spawn(listCondition, 0, false)
}

// enterStartPre runs ExecStartPre=, or goes on.
func (st *step) enterStartPre() {
	//: no pre-start command.
	if len(st.m.s.lists[listStartPre]) == 0 {
		st.enterStart()
		return
	}
	st.setState(coresvc.StateActivating, coresvc.SubStartPre)
	st.arm(timerState, st.m.s.timeoutStart)
	st.spawn(listStartPre, 0, false)
}

// enterStart runs the first ExecStart= as the main process — or, for
// Type=forking, as the command whose clean end makes the unit up.
func (st *step) enterStart() {
	//: a oneshot with nothing to run is up at once.
	if len(st.m.s.lists[listStart]) == 0 {
		st.enterStartPost()
		return
	}
	st.setState(coresvc.StateActivating, coresvc.SubStart)
	st.arm(timerState, st.m.s.timeoutStart)
	st.spawn(listStart, 0, st.m.s.typ != coresvc.TypeForking)
}

// enterStartPost starts the watchdog and runs ExecStartPost=, or goes on.
func (st *step) enterStartPost() {
	st.setState(coresvc.StateActivating, coresvc.SubStartPost)
	st.resetWatchdog()
	//: no post-start command.
	if len(st.m.s.lists[listStartPost]) == 0 {
		st.enterRunning(coresvc.ResultSuccess)
		return
	}
	st.arm(timerState, st.m.s.timeoutStart)
	st.spawn(listStartPost, 0, false)
}

// enterRunning is up, exited, or stopping, by what is left of the unit
// (service.c's service_enter_running).
func (st *step) enterRunning(f coresvc.Result) {
	m := st.m
	st.fold(f)
	st.dropControl()
	//: where the unit stands.
	switch {
	//: a failure folded in: stop, skipping ExecStop=.
	case m.result != coresvc.ResultSuccess:
		st.enterSignal(coresvc.SubStopSigterm, f)
	//: a main process — or a forking unit's cgroup with members and no main
	//: process found —, and a Type=dbus unit's name owned: up, unless it
	//: announced otherwise.
	case st.good():
		st.enterUp()
	//: a Type=dbus unit alive without its name: a grace to own it (S0e).
	case st.awaitsBusName():
		st.enterRevalidating()
	//: nothing runs: exited, or stopped.
	default:
		st.exitedOrStop()
	}
}

// exitedOrStop is service.c's service_enter_exited_or_stop: a unit with
// nothing left to be up on remains exited under RemainAfterExit=, or stops.
func (st *step) exitedOrStop() {
	m := st.m
	m.revalidateBegin = time.Time{}
	//: the unit remains.
	if m.s.on(setRemainAfterExit) {
		st.disarm(timerState)
		st.setState(coresvc.StateExited, coresvc.SubExited)
		return
	}
	st.enterStop(coresvc.ResultSuccess)
}

// enterUp is up with a main process: running, or the reload or stop it
// announced meanwhile.
func (st *step) enterUp() {
	m := st.m
	//: what the unit announced while starting.
	switch m.notify {
	//: a reload of its own.
	case notifyReloading:
		st.enterReloadByNotify()
	//: a stop of its own.
	case notifyStopping:
		st.enterStopByNotify()
	//: running, under RuntimeMaxSec=.
	default:
		st.resumeRuntime()
		st.runtimeDeadline()
		st.setState(coresvc.StateActive, coresvc.SubRunning)
		//: no runtime limit.
		if m.runtimeAt.IsZero() {
			st.disarm(timerState)
			return
		}
		st.armAt(timerState, m.runtimeAt)
	}
}

// runtimeDeadline sets when RuntimeMaxSec= ends the run, once, when the unit
// first comes up.
func (st *step) runtimeDeadline() {
	m := st.m
	//: unset, and a limit.
	if m.runtimeAt.IsZero() && m.s.runtimeMax != coresvc.Infinity {
		m.runtimeAt = st.ev.at.Add(addSaturating(m.s.runtimeMax, jitter(st.ev.rand, m.s.runtimeJitter)))
	}
}

// dropControl forgets a control process still running.
func (st *step) dropControl() {
	m := st.m
	//: one running.
	if m.controlPID != 0 {
		st.emit(&effect{kind: efForget, pid: m.controlPID})
		m.controlPID = 0
	}
	m.list = listNone
}

// enterStop runs ExecStop=, or signals at once.
func (st *step) enterStop(f coresvc.Result) {
	st.fold(f)
	st.dropControl()
	//: no stop command.
	if len(st.m.s.lists[listStop]) == 0 {
		st.enterSignal(coresvc.SubStopSigterm, coresvc.ResultSuccess)
		return
	}
	st.setState(coresvc.StateDeactivating, coresvc.SubStop)
	st.arm(timerState, st.m.s.timeoutStop)
	st.spawn(listStop, 0, false)
}

// enterStopByNotify is a STOPPING=1: stopping, no signal sent, under
// TimeoutStopSec=.
func (st *step) enterStopByNotify() {
	st.setState(coresvc.StateDeactivating, coresvc.SubStopSigterm)
	st.arm(timerState, st.m.s.timeoutStop)
}

// enterSignal sends a step's signal to what runs, and waits for it, or goes
// on at once when nothing runs. Under KillMode=control-group, and at mixed's
// kills, the signal reaches the unit's whole group (S0g).
func (st *step) enterSignal(sub coresvc.Sub, f coresvc.Result) {
	m := st.m
	s := m.s
	st.fold(f)
	//: the group: its outcome says whether anything is left to wait for.
	if st.groupSignals(sub) {
		st.signalGroup(sub)
		return
	}
	sig, extra := st.killSignal(sub)
	alive := (m.mainPID != 0 || m.controlPID != 0) && s.killMode != coresvc.KillModeNone
	//: KillMode=none signals nothing.
	if alive {
		//: the main process, then the control one.
		for _, pid := range []int{m.mainPID, m.controlPID} {
			st.signal(pid, sig)
			//: a terminating signal is followed by SIGCONT, and SIGHUP on request.
			for _, x := range extra {
				st.signal(pid, x)
			}
		}
		st.setState(coresvc.StateDeactivating, sub)
		st.arm(timerState, st.signalTimeout(sub))
		return
	}
	st.signalNothing(sub)
}

// signalTimeout is how long a signal step waits: TimeoutAbortSec= for an
// abort, TimeoutStopSec= otherwise.
func (st *step) signalTimeout(sub coresvc.Sub) time.Duration {
	s := st.m.s
	//: an abort.
	if sub == coresvc.SubStopWatchdog || sub == coresvc.SubFinalWatchdog {
		//: TimeoutAbortSec=.
		return s.timeoutAbort
	}
	//: TimeoutStopSec=.
	return s.timeoutStop
}

// signalNothing goes on from a signal step that found nothing to wait for.
func (st *step) signalNothing(sub coresvc.Sub) {
	s := st.m.s
	//: from the step.
	switch sub {
	//: the first stop signals: the final kill, or the post-stop commands.
	case coresvc.SubStopWatchdog, coresvc.SubStopSigterm:
		//: SendSIGKILL=.
		if s.on(setSendSIGKILL) {
			st.enterSignal(coresvc.SubStopSigkill, coresvc.ResultSuccess)
			return
		}
		st.enterStopPost(coresvc.ResultSuccess)
	//: the stop's final kill.
	case coresvc.SubStopSigkill:
		st.enterStopPost(coresvc.ResultSuccess)
	//: the final signals after the post-stop commands.
	case coresvc.SubFinalWatchdog, coresvc.SubFinalSigterm:
		//: SendSIGKILL=.
		if s.on(setSendSIGKILL) {
			st.enterSignal(coresvc.SubFinalSigkill, coresvc.ResultSuccess)
			return
		}
		st.enterDead(coresvc.ResultSuccess, true)
	//: the last kill.
	default:
		st.enterDead(coresvc.ResultSuccess, true)
	}
}

// killSignal is a step's signal, and those that follow it.
func (st *step) killSignal(sub coresvc.Sub) (coreproc.Signal, []coreproc.Signal) {
	s := st.m.s
	//: by the step.
	switch sub {
	//: an abort.
	case coresvc.SubStopWatchdog, coresvc.SubFinalWatchdog:
		//: WatchdogSignal=.
		return s.watchdogSig, nil
	//: a terminate.
	case coresvc.SubStopSigterm, coresvc.SubFinalSigterm:
		sig := s.killSignal
		//: a restart's stop.
		if st.m.has(flagRestarting) {
			sig = s.restartKillSignal
		}
		extra := []coreproc.Signal{s.sigcont}
		//: SendSIGHUP=.
		if s.on(setSendSIGHUP) {
			extra = append(extra, s.sighup)
		}
		//: the terminate, SIGCONT, SIGHUP.
		return sig, extra
	//: a kill.
	default:
		//: FinalKillSignal=.
		return s.finalKillSignal, nil
	}
}

// enterStopPost runs ExecStopPost=, or the final signals.
func (st *step) enterStopPost(f coresvc.Result) {
	st.fold(f)
	st.dropControl()
	//: no post-stop command.
	if len(st.m.s.lists[listStopPost]) == 0 {
		st.enterSignal(coresvc.SubFinalSigterm, coresvc.ResultSuccess)
		return
	}
	st.setState(coresvc.StateDeactivating, coresvc.SubStopPost)
	st.arm(timerState, st.m.s.timeoutStop)
	st.spawn(listStopPost, 0, false)
}

// enterDead ends the run: inactive, skipped or failed, then auto-restart when
// Restart= says so and nobody asked for the stop.
func (st *step) enterDead(f coresvc.Result, allowRestart bool) {
	m := st.m
	s := m.s
	st.fold(f)
	st.disarm(timerState)
	st.disarm(timerWatchdog)
	st.disarm(timerPIDFile)
	st.forgetAll()
	//: FileDescriptorStorePreserve=no: the store goes with every run.
	if s.preserve == coresvc.PreserveNo {
		st.releaseStore()
	}
	//: Delegate=: what the service made beneath the unit's group goes (S0g).
	if s.on(setDelegate) {
		st.emit(&effect{kind: efPrune})
	}
	m.list = listNone
	state, sub := endState(m.result)
	restart := allowRestart && !m.has(flagForbidRestart) && shallRestart(s, m.result, m.mainExit, m.has(flagMainExited))
	m.set(flagForbidRestart, false)
	//: no restart: dead.
	if !restart {
		st.setState(state, sub)
		return
	}
	//: RestartMode=normal and debug publish the failure first.
	if s.restartMode != coresvc.RestartModeDirect {
		st.setState(state, sub)
		st.publish()
	}
	next := m.restarts
	//: the restart counter, held below overflow.
	if next < ^uint32(0) {
		next++
	}
	st.setState(coresvc.StateAutoRestart, coresvc.SubAutoRestart)
	st.arm(timerState, addSaturating(restartDelay(s, next), jitter(st.ev.rand, s.restartJitter)))
}

// endState is the state a run that ended with result rests in: inactive
// after a success, skipped after a skipped condition, failed otherwise.
func endState(result coresvc.Result) (coresvc.State, coresvc.Sub) {
	//: a clean end.
	if result == coresvc.ResultSuccess {
		//: inactive.
		return coresvc.StateInactive, coresvc.SubDead
	}
	//: a skipped condition.
	if result == coresvc.ResultExecCondition {
		//: skipped.
		return coresvc.StateConditionSkipped, coresvc.SubDead
	}
	//: a failure.
	return coresvc.StateFailed, coresvc.SubFailed
}

// enterRestart is the restart delay's end: an automatic start.
func (st *step) enterRestart() {
	m := st.m
	//: counted, held below overflow.
	if m.restarts < ^uint32(0) {
		m.restarts++
	}
	st.beginStart(false)
}

// enterReload runs ExecReload=, or signals the main process.
func (st *step) enterReload() {
	m := st.m
	m.reloadResult, m.reloadBegin = coresvc.ResultSuccess, st.ev.monotonic
	st.setState(coresvc.StateReloading, coresvc.SubReload)
	st.arm(timerState, m.s.timeoutStart)
	//: no reload command: the signal.
	if len(m.s.lists[listReload]) == 0 {
		st.reloadSignal()
		return
	}
	st.spawn(listReload, 0, false)
}

// enterReloadByNotify is a RELOADING=1 the engine did not ask for.
func (st *step) enterReloadByNotify() {
	st.m.reloadResult = coresvc.ResultSuccess
	st.setState(coresvc.StateReloading, coresvc.SubReloadNotify)
	st.arm(timerState, st.m.s.timeoutStart)
}

// reloadCommandsDone goes on once ExecReload= ended with f.
func (st *step) reloadCommandsDone(f coresvc.Result) {
	m := st.m
	//: a reload command failed: the reload failed, the unit runs on.
	if f != coresvc.ResultSuccess {
		m.reloadResult = f
		st.enterRunning(coresvc.ResultSuccess)
		return
	}
	//: the unit announced its reload already: wait for READY=1.
	if m.notify == notifyReloading {
		st.setState(coresvc.StateReloading, coresvc.SubReloadNotify)
		return
	}
	st.reloadSignal()
}

// reloadSignal sends ReloadSignal= to a notify-reload unit's main process and
// waits for RELOADING=1; any other unit has reloaded.
func (st *step) reloadSignal() {
	m := st.m
	//: the notify-reload protocol.
	if m.s.typ == coresvc.TypeNotifyReload && m.mainPID != 0 {
		st.signal(m.mainPID, m.s.reloadSignal)
		st.setState(coresvc.StateReloading, coresvc.SubReloadSignal)
		return
	}
	st.reloadDone()
}

// reloadDone runs ExecReloadPost= after a successful reload, or is up again.
func (st *step) reloadDone() {
	//: no post-reload command.
	if len(st.m.s.lists[listReloadPost]) == 0 {
		st.enterRunning(coresvc.ResultSuccess)
		return
	}
	st.setState(coresvc.StateReloading, coresvc.SubReloadPost)
	st.spawn(listReloadPost, 0, false)
}

// watchdogRuns reports whether the watchdog runs at a step: from the
// start-up's end to the stop's, as service.c keeps it.
func watchdogRuns(sub coresvc.Sub) bool {
	//: the steps the watchdog runs in.
	switch sub {
	//: up, reloading, stopping.
	case coresvc.SubStartPost, coresvc.SubRunning, coresvc.SubRunningRevalidating, coresvc.SubReload,
		coresvc.SubReloadSignal, coresvc.SubReloadNotify, coresvc.SubReloadPost, coresvc.SubStop,
		coresvc.SubStopSigterm, coresvc.SubStopSigkill, coresvc.SubStopPost, coresvc.SubFinalSigterm,
		coresvc.SubFinalSigkill:
		//: runs.
		return true
	//: every other step.
	default:
		//: does not.
		return false
	}
}

// watchdogPeriod is WATCHDOG_USEC='s override, else WatchdogSec=.
func (st *step) watchdogPeriod() time.Duration {
	//: an override.
	if st.m.watchdogPeriod > 0 {
		//: WATCHDOG_USEC=.
		return st.m.watchdogPeriod
	}
	//: WatchdogSec=, 0 for none.
	return st.m.s.watchdog
}

// resetWatchdog re-arms the keep-alive deadline.
func (st *step) resetWatchdog() {
	period := st.watchdogPeriod()
	//: no watchdog, or not at a step it runs in.
	if period <= 0 || !watchdogRuns(st.m.sub) {
		st.disarm(timerWatchdog)
		return
	}
	st.arm(timerWatchdog, period)
}

// forceWatchdog aborts the unit with WatchdogSignal=: a missed keep-alive,
// WATCHDOG=trigger, or Fail. It acts until the stop is under way.
func (st *step) forceWatchdog() {
	m := st.m
	//: nothing to abort, or a stop already signalling.
	if isDead(m.state) || m.state == coresvc.StateAutoRestart || (m.state == coresvc.StateDeactivating && m.sub != coresvc.SubStop) {
		return
	}
	st.enterSignal(coresvc.SubStopWatchdog, coresvc.ResultWatchdog)
}
