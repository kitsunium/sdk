package service

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// The cgroup modes (S0g, kitsunium/sdk#734) are service.c's handling of the
// unit's cgroup: unit_kill_context's cgroup kill for KillMode=control-group
// and mixed, service_notify_cgroup_empty_event for a group that emptied,
// service_notify_cgroup_oom_event for an OOM kill. The shell reads the group
// whenever an event may decide on it — a process's end, the watch's change, a
// group signal — so the reducer never acts on what a late event says.

// groupSignals reports whether a signal step reaches the unit's whole group:
// every step under KillMode=control-group, the kills under mixed.
func (st *step) groupSignals(sub coresvc.Sub) bool {
	//: by the kill mode.
	switch st.m.s.killMode {
	//: every step.
	case coresvc.KillModeControlGroup:
		//: the group.
		return true
	//: the main and control processes, then the group at the kill.
	case coresvc.KillModeMixed:
		//: the kills.
		return sub == coresvc.SubStopSigkill || sub == coresvc.SubFinalSigkill
	//: process and none.
	default:
		//: the processes alone.
		return false
	}
}

// signalGroup sends a step's signals to the unit's group — the main and
// control processes through their handles where they are outside it — and
// waits under the step's timeout: the outcome, evSignaled, goes on at once
// when the group held no process (unit_kill_context's wait_for_exit).
func (st *step) signalGroup(sub coresvc.Sub) {
	m := st.m
	sig, extra := st.killSignal(sub)
	st.setState(coresvc.StateDeactivating, sub)
	st.arm(timerState, st.signalTimeout(sub))
	st.emit(&effect{
		kind: efSignalGroup, sub: sub,
		signals: append([]coreproc.Signal{sig}, extra...),
		pids:    []int{m.mainPID, m.controlPID},
	})
}

// onSignaled takes a group signal's outcome: a group that held no process,
// with no main or control process left, has nothing to wait for — the step
// goes on as one that found nothing to signal. An outcome for a step the
// unit left is stale.
func (st *step) onSignaled() {
	m := st.m
	ev := st.ev
	st.population()
	//: the step left, something to wait for, or a group not known empty.
	if ev.sub != m.sub || m.mainPID != 0 || m.controlPID != 0 || !ev.has(flagEmpty) {
		return
	}
	st.signalNothing(m.sub)
}

// onCgroup takes a change of the unit's group: an OOM kill first, then its
// population, then — empty — what an emptied group ends.
func (st *step) onCgroup() {
	st.groupNews()
	//: emptied.
	if st.ev.has(flagEmpty) {
		st.onCgroupEmpty()
	}
}

// groupNews takes what the shell read of the unit's group with an event: an
// OOM kill — before the event itself, as systemd dispatches its OOM queue
// before SIGCHLD —, then the population.
func (st *step) groupNews() {
	//: an OOM kill the reducer has not seen.
	if st.ev.has(flagOOM) {
		st.onOOM()
	}
	st.population()
}

// population records what the shell read of the group: populated or empty;
// an event that read nothing leaves it.
func (st *step) population() {
	ev := st.ev
	//: read.
	if ev.has(flagPopulated) || ev.has(flagEmpty) {
		st.m.set(flagGroupPopulated, ev.has(flagPopulated))
	}
}

// onOOM is service_notify_cgroup_oom_event: OOMPolicy=continue keeps the unit;
// stop stops it — signalled while starting, ExecStop= once up —, kill sends
// the final kill at once; either ends it with the result oom-kill.
func (st *step) onOOM() {
	m := st.m
	policy := m.s.oomPolicy
	//: the unit keeps running.
	if policy == coresvc.OOMPolicyContinue {
		return
	}
	kill := policy == coresvc.OOMPolicyKill
	//: by the step.
	switch m.sub {
	//: starting, or running ExecStop=.
	case coresvc.SubCondition, coresvc.SubStartPre, coresvc.SubStart, coresvc.SubStartPost, coresvc.SubStop:
		st.stopOrKill(kill, func() { st.enterSignal(coresvc.SubStopSigterm, coresvc.ResultOOMKill) })
	//: up — a Type=dbus unit awaiting its name included (S0e).
	case coresvc.SubRunning, coresvc.SubRunningRevalidating, coresvc.SubExited:
		st.stopOrKill(kill, func() { st.enterStop(coresvc.ResultOOMKill) })
	//: terminating: killed now.
	case coresvc.SubStopWatchdog, coresvc.SubStopSigterm:
		st.enterSignal(coresvc.SubStopSigkill, coresvc.ResultOOMKill)
	//: being killed: the result alone.
	case coresvc.SubStopSigkill, coresvc.SubFinalSigkill:
		st.fold(coresvc.ResultOOMKill)
	//: after the main process: the final kill.
	case coresvc.SubStopPost, coresvc.SubFinalSigterm:
		st.enterSignal(coresvc.SubFinalSigkill, coresvc.ResultOOMKill)
	//: reloading, dead, waiting to restart, the final abort.
	default:
	}
}

// stopOrKill sends the final kill for OOMPolicy=kill, or runs stop.
func (st *step) stopOrKill(kill bool, stop func()) {
	//: the whole group, at once.
	if kill {
		st.enterSignal(coresvc.SubFinalSigkill, coresvc.ResultOOMKill)
		return
	}
	stop()
}

// onCgroupEmpty is service_notify_cgroup_empty_event: the group emptied.
// Starting, a notify unit nobody is left to make ready fails with protocol,
// an ExitType=cgroup unit is over, a PID file nobody is left to write is
// given up; up on the group alone, the unit stops; stopping with no main or
// control process left, the stop goes on.
func (st *step) onCgroupEmpty() {
	m := st.m
	idle := m.mainPID == 0 && m.controlPID == 0
	//: by the step.
	switch m.sub {
	//: starting.
	case coresvc.SubStart:
		st.emptiedStarting(idle)
	//: after ExecStartPost=.
	case coresvc.SubStartPost:
		st.pidFileAbandoned()
	//: up on the group alone: no longer.
	case coresvc.SubRunning, coresvc.SubRunningRevalidating:
		//: a main process keeps it up.
		if m.mainPID == 0 {
			m.set(flagCgroupGood, false)
			st.enterRunning(coresvc.ResultSuccess)
		}
	//: the first stop signals: the post-stop commands.
	case coresvc.SubStopWatchdog, coresvc.SubStopSigterm, coresvc.SubStopSigkill:
		st.whenIdle(idle, func() { st.enterStopPost(coresvc.ResultSuccess) })
	//: the final signals: dead.
	case coresvc.SubStopPost, coresvc.SubFinalWatchdog, coresvc.SubFinalSigterm, coresvc.SubFinalSigkill:
		st.whenIdle(idle, func() { st.enterDead(coresvc.ResultSuccess, true) })
	//: dead, up with nothing to wait for, reloading.
	default:
	}
}

// emptiedStarting is an emptied group while ExecStart= runs or the unit
// waits for its main process.
func (st *step) emptiedStarting(idle bool) {
	m := st.m
	//: by what the start waits for.
	switch {
	//: READY=1, which nobody is left to send.
	case isNotifyType(m.s.typ) && idle:
		st.enterStopPost(coresvc.ResultProtocol)
	//: the group's end, which this is.
	case m.s.exitType == coresvc.ExitTypeCgroup && m.mainPID == 0:
		st.enterStopPost(coresvc.ResultSuccess)
	//: a PID file, which nobody is left to write.
	default:
		st.pidFileAbandoned()
	}
}

// pidFileAbandoned gives up a PID file nobody is left to write.
func (st *step) pidFileAbandoned() {
	//: a forking unit waiting for its PID file.
	if st.m.s.pidFile != "" && st.waitingForMain() {
		st.mainNeverFound()
	}
}

// whenIdle runs next when no main or control process is left.
func (st *step) whenIdle(idle bool, next func()) {
	//: nothing left to wait for.
	if idle {
		next()
	}
}
