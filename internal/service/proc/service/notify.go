package service

import (
	"strconv"
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// notifyAccessNames reads NOTIFYACCESS=.
var notifyAccessNames = map[string]coresvc.NotifyAccess{
	"none": coresvc.NotifyAccessNone, "main": coresvc.NotifyAccessMain,
	"exec": coresvc.NotifyAccessExec, "all": coresvc.NotifyAccessAll,
}

// onNotify takes one sd_notify message (sd_notify(3)), in service.c's order:
// who may send it, MAINPID=, NOTIFYACCESS=, then the states, the metadata,
// the watchdog, the timeouts, the descriptor store.
func (st *step) onNotify() {
	m := st.m
	msg := st.ev.msg
	//: a sender the kernel did not name, or one NotifyAccess= does not admit:
	//: nothing it sent is read, and its descriptors are closed.
	if !st.admitted() {
		m.notifyRefused++
		st.closeFiles(msg.Files)
		return
	}
	f := msg.Fields
	st.notifyMainPID(f)
	st.notifyAccessOverride(f)
	st.notifyStates(f)
	st.notifyMetadata(f)
	st.notifyWatchdog(f)
	st.notifyTimeouts(f)
	//: RESTART_RESET=1 flushes the restart counter.
	if f["RESTART_RESET"] == "1" {
		m.restarts = 0
	}
	st.notifyStore(f)
}

// admitted is NotifyAccess=: whose messages are read.
func (st *step) admitted() bool {
	m := st.m
	msg := st.ev.msg
	//: an unverified sender is nobody.
	if !msg.Verified || msg.PID <= 0 {
		//: refused.
		return false
	}
	//: by the effective access.
	switch m.notifyAccess {
	//: the main process.
	case coresvc.NotifyAccessMain:
		//: its PID.
		return msg.PID == m.mainPID
	//: the main process and the command running.
	case coresvc.NotifyAccessExec:
		//: either PID.
		return msg.PID == m.mainPID || msg.PID == m.controlPID
	//: any process of the unit's cgroup.
	case coresvc.NotifyAccessAll:
		//: the shell asked the cgroup.
		return st.ev.has(flagMember)
	//: none.
	default:
		//: refused.
		return false
	}
}

// notifyMainPID takes MAINPID=: a new main process, adopted, refused when it
// is no process of the unit's.
func (st *step) notifyMainPID(f map[string]string) {
	m := st.m
	v, ok := f["MAINPID"]
	//: none, or at a step where the main process cannot change.
	if !ok || !mainPIDChanges(m.sub) {
		return
	}
	pid, err := strconv.Atoi(v)
	//: the main process already.
	if err == nil && pid == m.mainPID {
		return
	}
	//: a process the engine may not take as the unit's main.
	if err != nil || !st.mainPIDAcceptable(pid) {
		m.notifyRefused++
		return
	}
	st.emit(&effect{kind: efAttach, pid: pid})
}

// mainPIDAcceptable reports whether pid may be the unit's main process: in
// its cgroup, or — without one — named by a privileged sender; never the
// engine, never PID 1, never one the engine cannot watch. A sandboxed unit's
// uid 0 is no privilege — root of its own user namespace, or confined —, so
// without a cgroup it names no main process (#733).
func (st *step) mainPIDAcceptable(pid int) bool {
	s := st.m.s
	privileged := !s.on(setHasCgroup) && !s.on(setSandboxed) && st.ev.msg.UID == 0
	member := st.ev.has(flagMainMember) || privileged
	//: the rules, all at once.
	return pid > 1 && pid != s.selfPID && s.on(setHasAttacher) && member
}

// mainPIDChanges reports whether MAINPID= is read at a step.
func mainPIDChanges(sub coresvc.Sub) bool {
	//: from the start to the stop's signals.
	switch sub {
	//: read.
	case coresvc.SubStart, coresvc.SubStartPost, coresvc.SubRunning, coresvc.SubRunningRevalidating,
		coresvc.SubReload, coresvc.SubReloadSignal, coresvc.SubReloadNotify, coresvc.SubReloadPost,
		coresvc.SubStop, coresvc.SubStopWatchdog, coresvc.SubStopSigterm, coresvc.SubStopSigkill:
		//: yes.
		return true
	//: every other step.
	default:
		//: no.
		return false
	}
}

// notifyAccessOverride takes NOTIFYACCESS=.
func (st *step) notifyAccessOverride(f map[string]string) {
	m := st.m
	v, ok := f["NOTIFYACCESS"]
	//: none.
	if !ok {
		return
	}
	access, known := notifyAccessNames[v]
	//: an unknown value, or one without the cgroup it needs.
	if !known || ((access == coresvc.NotifyAccessExec || access == coresvc.NotifyAccessAll) && !m.s.on(setHasCgroup)) {
		m.notifyRefused++
		return
	}
	m.notifyAccess = access
}

// notifyStates takes RELOADING=1, READY=1 and STOPPING=1, in that order.
func (st *step) notifyStates(f map[string]string) {
	reloading := f["RELOADING"] == "1"
	inCycle := st.reloadCycle(f)
	//: a reload begins.
	if reloading {
		st.notifyReloading(inCycle)
	}
	//: up, or reloaded.
	if f["READY"] == "1" {
		st.notifyReady(reloading && inCycle)
	}
	//: a stop of the unit's own, which is final.
	if f["STOPPING"] == "1" {
		st.notifyStopping()
	}
}

// notifyReloading takes RELOADING=1: the answer to ReloadSignal= when it
// belongs to the reload asked for, or a reload of the unit's own.
func (st *step) notifyReloading(inCycle bool) {
	m := st.m
	m.notify = notifyReloading
	//: the answer to ReloadSignal=.
	if m.sub == coresvc.SubReloadSignal && inCycle {
		st.setState(coresvc.StateReloading, coresvc.SubReloadNotify)
		return
	}
	//: unasked.
	if m.sub == coresvc.SubRunning {
		st.enterReloadByNotify()
	}
}

// notifyReady takes READY=1: a notify type's start-up over, a reload over —
// or, with RELOADING=1 in the same message, a reload begun and ended.
func (st *step) notifyReady(reloadedNow bool) {
	m := st.m
	m.notify = notifyReady
	//: by the step.
	switch {
	//: a notify type's start-up is over.
	case m.sub == coresvc.SubStart && isNotifyType(m.s.typ):
		st.enterStartPost()
	//: a reload is over.
	case m.sub == coresvc.SubReloadNotify:
		st.reloadDone()
	//: RELOADING=1 and READY=1 at once.
	case reloadedNow && (m.sub == coresvc.SubReload || m.sub == coresvc.SubReloadSignal):
		st.dropControl()
		st.reloadDone()
	//: any other step.
	default:
	}
}

// notifyStopping takes STOPPING=1: deactivating, under TimeoutStopSec=.
func (st *step) notifyStopping() {
	m := st.m
	m.notify = notifyStopping
	//: up — a Type=dbus unit awaiting its name included (S0e).
	if m.sub == coresvc.SubRunning || m.sub == coresvc.SubRunningRevalidating {
		st.enterStopByNotify()
	}
}

// reloadCycle reports whether a RELOADING=1 belongs to the reload the engine
// asked for: its MONOTONIC_USEC= is not before the reload began. Where the
// shell could not read CLOCK_MONOTONIC the order of the messages decides.
func (st *step) reloadCycle(f map[string]string) bool {
	m := st.m
	//: no begin known.
	if m.reloadBegin == 0 {
		//: in order.
		return true
	}
	mono, err := strconv.ParseUint(f["MONOTONIC_USEC"], 10, 64)
	//: the stamp, after the begin.
	return err == nil && mono >= m.reloadBegin
}

// notifyMetadata takes STATUS=, ERRNO=, BUSERROR=, VARLINKERROR= and
// EXIT_STATUS=, which only describe the unit.
func (st *step) notifyMetadata(f map[string]string) {
	m := st.m
	//: STATUS=.
	if v, ok := f["STATUS"]; ok {
		m.status = v
	}
	//: ERRNO=, a number.
	if v, ok := f["ERRNO"]; ok {
		//: a valid errno.
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			m.errno = n
		}
	}
	//: BUSERROR=.
	if v, ok := f["BUSERROR"]; ok {
		m.busError = v
	}
	//: VARLINKERROR=.
	if v, ok := f["VARLINKERROR"]; ok {
		m.varlinkError = v
	}
	//: EXIT_STATUS=, informative.
	if v, ok := f["EXIT_STATUS"]; ok {
		m.exitStatusText = v
	}
}

// notifyWatchdog takes WATCHDOG=1, WATCHDOG=trigger and WATCHDOG_USEC=.
func (st *step) notifyWatchdog(f map[string]string) {
	m := st.m
	//: a new period, which re-arms the deadline.
	if v, ok := f["WATCHDOG_USEC"]; ok {
		//: a valid count.
		if d, valid := parseUsec(v); valid && d > 0 {
			m.watchdogPeriod = d
			st.resetWatchdog()
		}
	}
	//: by the value.
	switch f["WATCHDOG"] {
	//: the keep-alive.
	case "1":
		//: armed: re-armed.
		if !m.deadlines[timerWatchdog].IsZero() {
			st.resetWatchdog()
		}
	//: an error the unit found itself.
	case "trigger":
		st.forceWatchdog()
	}
}

// notifyTimeouts takes EXTEND_TIMEOUT_USEC=: the step's timeout and the
// keep-alive deadline pushed to now + the value, never earlier.
func (st *step) notifyTimeouts(f map[string]string) {
	m := st.m
	v, ok := f["EXTEND_TIMEOUT_USEC"]
	//: none.
	if !ok {
		return
	}
	d, valid := parseUsec(v)
	//: not a count.
	if !valid {
		return
	}
	at := st.ev.at.Add(d)
	//: each armed timer.
	for k := range timerCount {
		//: armed, and the extension later than its deadline.
		if !m.deadlines[k].IsZero() && at.After(m.deadlines[k]) && (k != timerState || m.sub != coresvc.SubAutoRestart) {
			st.armAt(k, at)
		}
	}
}

// parseUsec reads a microsecond count.
func parseUsec(v string) (time.Duration, bool) {
	n, err := strconv.ParseUint(v, 10, 63)
	//: not a count, or past the longest duration.
	if err != nil || n > uint64(coresvc.Infinity/time.Microsecond) {
		//: refused.
		return 0, false
	}
	//: the duration.
	return time.Duration(n) * time.Microsecond, true
}
