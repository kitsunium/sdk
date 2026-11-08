package service

import (
	"time"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// timeouts are what a step does when its timer fires (service.c's
// service_dispatch_timer); every row's result is timeout, but a Type=dbus
// unit's grace (S0e): its name still not owned, it is exited or stopped
// (service_dispatch_bus_name_grace).
var timeouts = map[coresvc.Sub]func(st *step){
	coresvc.SubCondition:           (*step).startTimedOut,
	coresvc.SubStartPre:            (*step).startTimedOut,
	coresvc.SubStart:               (*step).startTimedOut,
	coresvc.SubStartPost:           (*step).startTimedOut,
	coresvc.SubRunning:             func(st *step) { st.enterStop(coresvc.ResultTimeout) },
	coresvc.SubReload:              (*step).reloadTimedOut,
	coresvc.SubReloadSignal:        (*step).reloadTimedOut,
	coresvc.SubReloadNotify:        (*step).reloadTimedOut,
	coresvc.SubReloadPost:          (*step).reloadTimedOut,
	coresvc.SubStop:                func(st *step) { st.failureMode(st.m.s.stopMode) },
	coresvc.SubStopWatchdog:        func(st *step) { st.killOrPost(coresvc.ResultTimeout) },
	coresvc.SubStopSigterm:         (*step).sigtermTimedOut,
	coresvc.SubStopSigkill:         func(st *step) { st.enterStopPost(coresvc.ResultTimeout) },
	coresvc.SubStopPost:            (*step).stopPostTimedOut,
	coresvc.SubFinalWatchdog:       func(st *step) { st.finalKillOrDead(coresvc.ResultTimeout) },
	coresvc.SubFinalSigterm:        (*step).finalSigtermTimedOut,
	coresvc.SubFinalSigkill:        func(st *step) { st.enterDead(coresvc.ResultTimeout, true) },
	coresvc.SubAutoRestart:         (*step).enterRestart,
	coresvc.SubRunningRevalidating: (*step).exitedOrStop,
}

// onTimer takes a timer's fire, unless a later arming made it stale.
func (st *step) onTimer() {
	m := st.m
	ev := st.ev
	//: a fire of an older arming, or of a disarmed timer.
	if ev.seq != m.seq[ev.timer] || m.deadlines[ev.timer].IsZero() {
		return
	}
	m.deadlines[ev.timer] = time.Time{}
	//: the keep-alive deadline passed.
	if ev.timer == timerWatchdog {
		st.forceWatchdog()
		return
	}
	//: a forking unit's PID file is due again.
	if ev.timer == timerPIDFile {
		st.pidFileDue()
		return
	}
	//: a step with a timeout.
	if next, ok := timeouts[m.sub]; ok {
		next(st)
	}
}

// startTimedOut is a start step that timed out: TimeoutStartFailureMode=.
func (st *step) startTimedOut() {
	st.failureMode(st.m.s.startMode)
}

// reloadTimedOut is a reload that timed out: its command killed, the reload
// failed, the unit running on.
func (st *step) reloadTimedOut() {
	m := st.m
	st.signal(m.controlPID, sigKILL)
	m.reloadResult = coresvc.ResultTimeout
	st.enterRunning(coresvc.ResultSuccess)
}

// sigtermTimedOut is the terminate that did not end the unit: the abort, or
// the final kill.
func (st *step) sigtermTimedOut() {
	//: TimeoutStopFailureMode=abort.
	if st.m.s.stopMode == coresvc.TimeoutAbort {
		st.enterSignal(coresvc.SubStopWatchdog, coresvc.ResultTimeout)
		return
	}
	st.killOrPost(coresvc.ResultTimeout)
}

// stopPostTimedOut is ExecStopPost= that timed out: TimeoutStopFailureMode=.
func (st *step) stopPostTimedOut() {
	//: by the mode.
	switch st.m.s.stopMode {
	//: abort.
	case coresvc.TimeoutAbort:
		st.enterSignal(coresvc.SubFinalWatchdog, coresvc.ResultTimeout)
	//: kill.
	case coresvc.TimeoutKill:
		st.finalKillOrDead(coresvc.ResultTimeout)
	//: terminate.
	default:
		st.enterSignal(coresvc.SubFinalSigterm, coresvc.ResultTimeout)
	}
}

// finalSigtermTimedOut is the final terminate that did not end what was left.
func (st *step) finalSigtermTimedOut() {
	//: TimeoutStopFailureMode=abort.
	if st.m.s.stopMode == coresvc.TimeoutAbort {
		st.enterSignal(coresvc.SubFinalWatchdog, coresvc.ResultTimeout)
		return
	}
	st.finalKillOrDead(coresvc.ResultTimeout)
}

// failureMode enters the stop step a failure mode names, with the result
// timeout.
func (st *step) failureMode(mode coresvc.TimeoutMode) {
	//: by the mode.
	switch mode {
	//: abort: WatchdogSignal=.
	case coresvc.TimeoutAbort:
		st.enterSignal(coresvc.SubStopWatchdog, coresvc.ResultTimeout)
	//: kill: FinalKillSignal= at once, or the post-stop without it.
	case coresvc.TimeoutKill:
		st.killOrPost(coresvc.ResultTimeout)
	//: terminate.
	default:
		st.enterSignal(coresvc.SubStopSigterm, coresvc.ResultTimeout)
	}
}

// killOrPost is the stop's final kill, or the post-stop without one.
func (st *step) killOrPost(f coresvc.Result) {
	//: SendSIGKILL=.
	if st.m.s.on(setSendSIGKILL) {
		st.enterSignal(coresvc.SubStopSigkill, f)
		return
	}
	st.enterStopPost(f)
}

// finalKillOrDead is the last kill, or dead without one.
func (st *step) finalKillOrDead(f coresvc.Result) {
	//: SendSIGKILL=.
	if st.m.s.on(setSendSIGKILL) {
		st.enterSignal(coresvc.SubFinalSigkill, f)
		return
	}
	st.enterDead(f, false)
}
