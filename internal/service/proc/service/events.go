package service

import (
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
)

// A process's end is service.c's service_sigchld_event: each table holds one
// row per step, so a transition is read where systemd writes it. A timer's
// fire is timeout.go's, a spawn's outcome spawned.go's.

var (
	// mainExits are what a step does once its main process ended with f;
	// a step with no row waits for its command. A Type=dbus unit awaiting
	// its name (S0e) reads as running.
	mainExits = map[coresvc.Sub]func(st *step, f coresvc.Result){
		coresvc.SubStart:               (*step).mainExitedStarting,
		coresvc.SubRunning:             (*step).enterRunning,
		coresvc.SubRunningRevalidating: (*step).enterRunning,
		coresvc.SubStopWatchdog:        func(st *step, f coresvc.Result) { st.whenNoControl(func() { st.enterStopPost(f) }) },
		coresvc.SubStopSigterm:         func(st *step, f coresvc.Result) { st.whenNoControl(func() { st.enterStopPost(f) }) },
		coresvc.SubStopSigkill:         func(st *step, f coresvc.Result) { st.whenNoControl(func() { st.enterStopPost(f) }) },
		coresvc.SubStopPost: func(st *step, f coresvc.Result) {
			st.whenNoControl(func() { st.enterSignal(coresvc.SubFinalSigterm, f) })
		},
		coresvc.SubFinalWatchdog: func(st *step, f coresvc.Result) { st.whenNoControl(func() { st.enterDead(f, true) }) },
		coresvc.SubFinalSigterm:  func(st *step, f coresvc.Result) { st.whenNoControl(func() { st.enterDead(f, true) }) },
		coresvc.SubFinalSigkill:  func(st *step, f coresvc.Result) { st.whenNoControl(func() { st.enterDead(f, true) }) },
	}
	// controlDone are what a step does once its commands ended with f.
	controlDone = map[coresvc.Sub]func(st *step, f coresvc.Result){
		coresvc.SubCondition:     func(st *step, f coresvc.Result) { st.onOK(f, st.enterStartPre) },
		coresvc.SubStartPre:      func(st *step, f coresvc.Result) { st.onOK(f, st.enterStart) },
		coresvc.SubStart:         (*step).forkingStarted,
		coresvc.SubStartPost:     func(st *step, f coresvc.Result) { st.onOK(f, st.startPostDone) },
		coresvc.SubReload:        (*step).reloadCommandsDone,
		coresvc.SubReloadPost:    (*step).reloadPostDone,
		coresvc.SubStop:          func(st *step, f coresvc.Result) { st.enterSignal(coresvc.SubStopSigterm, f) },
		coresvc.SubStopWatchdog:  func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterStopPost(f) }) },
		coresvc.SubStopSigterm:   func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterStopPost(f) }) },
		coresvc.SubStopSigkill:   func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterStopPost(f) }) },
		coresvc.SubStopPost:      func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterSignal(coresvc.SubFinalSigterm, f) }) },
		coresvc.SubFinalWatchdog: func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterDead(f, true) }) },
		coresvc.SubFinalSigterm:  func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterDead(f, true) }) },
		coresvc.SubFinalSigkill:  func(st *step, f coresvc.Result) { st.whenNoMain(func() { st.enterDead(f, true) }) },
	}
)

// whenNoControl runs next once no command runs.
func (st *step) whenNoControl(next func()) {
	//: the command is gone too.
	if st.m.controlPID == 0 {
		next()
	}
}

// whenNoMain runs next once the main process is gone.
func (st *step) whenNoMain(next func()) {
	//: the main process is gone too.
	if st.m.mainPID == 0 {
		next()
	}
}

// onExit takes a process's end: the main process's, a command's, or a stale
// one's, which nothing reads — after what the shell read of the unit's group
// with it (S0g): an OOM kill first, then the population.
func (st *step) onExit() {
	m := st.m
	pid := st.ev.pid
	st.groupNews()
	//: no process.
	if pid == 0 {
		return
	}
	//: the main process.
	if pid == m.mainPID {
		st.mainExited()
		return
	}
	//: the command running.
	if pid == m.controlPID {
		st.controlExited()
	}
}

// mainExited is service.c's main-process branch of service_sigchld_event.
func (st *step) mainExited() {
	m := st.m
	ev := st.ev
	m.mainPID, m.mainExit = 0, ev.exit
	m.set(flagMainExited, true)
	//: ExitType=cgroup: the group, not the main process, keeps the unit up.
	if m.s.exitType == coresvc.ExitTypeCgroup {
		m.set(flagCgroupGood, m.has(flagGroupPopulated))
	}
	f := classifyMain(m.s, ev.exit, ev.has(flagUnknown), m.mainFlags)
	st.fold(f)
	//: a oneshot runs its next command after a success.
	if st.oneshotHasNext(f) {
		st.spawn(listStart, m.cmd+1, true)
		return
	}
	//: a step with a row; the others wait for their command.
	if next, ok := mainExits[m.sub]; ok {
		next(st, f)
	}
}

// oneshotHasNext reports a oneshot whose command succeeded and has another.
func (st *step) oneshotHasNext(f coresvc.Result) bool {
	m := st.m
	//: a oneshot starting, a success, a command left.
	return m.s.typ == coresvc.TypeOneshot && m.sub == coresvc.SubStart && f == coresvc.ResultSuccess &&
		m.cmd+1 < len(m.s.lists[listStart])
}

// mainExitedStarting takes a main process that ended before the unit was up.
func (st *step) mainExitedStarting(f coresvc.Result) {
	m := st.m
	ok := f == coresvc.ResultSuccess
	//: by the type.
	switch {
	//: a oneshot is up once its last command succeeded.
	case m.s.typ == coresvc.TypeOneshot:
		st.onOK(f, st.enterStartPost)
	//: a forking unit's daemon, named by MAINPID= and gone: its end is folded,
	//: and the start waits for its command.
	case m.s.typ == coresvc.TypeForking:
	//: a notify type's failure.
	case isNotifyType(m.s.typ) && !ok:
		st.enterSignal(coresvc.SubStopSigterm, f)
	//: a notify type is up only through READY=1: a protocol failure, unless
	//: another process may still send it.
	case isNotifyType(m.s.typ):
		st.notifyMainGone()
	//: simple, exec, idle: already up by now.
	default:
		st.enterRunning(f)
	}
}

// notifyMainGone fails a notify unit whose main process ended cleanly before
// READY=1, unless it remains and another process may still send it.
func (st *step) notifyMainGone() {
	m := st.m
	//: somebody may still say READY=1.
	if m.s.on(setRemainAfterExit) && m.notifyAccess != coresvc.NotifyAccessMain {
		return
	}
	st.enterSignal(coresvc.SubStopSigterm, coresvc.ResultProtocol)
}

// controlExited is service.c's control-process branch of
// service_sigchld_event.
func (st *step) controlExited() {
	m := st.m
	ev := st.ev
	m.controlPID = 0
	f := classifyControl(ev.exit, ev.has(flagUnknown), st.runningFlags())
	//: ExecCondition=: 1 to 254 skips, 255 and an abnormal end fail.
	if m.sub == coresvc.SubCondition && f == coresvc.ResultExitCode && ev.exit.Code < maxExitCode {
		f = coresvc.ResultExecCondition
	}
	//: the list's next command after a success.
	if f == coresvc.ResultSuccess && m.list != listNone && m.cmd+1 < len(m.s.lists[m.list]) {
		st.spawn(m.list, m.cmd+1, false)
		return
	}
	m.list = listNone
	//: a step with a row.
	if next, ok := controlDone[m.sub]; ok {
		next(st, f)
	}
}

// runningFlags are the prefixes of the command running, none when the step
// no longer runs its list.
func (st *step) runningFlags() coresvc.CommandFlags {
	m := st.m
	//: no list, or past its end.
	if m.list == listNone || m.cmd >= len(m.s.lists[m.list]) {
		//: none.
		return 0
	}
	//: the command's.
	return m.s.lists[m.list][m.cmd].Flags
}

// onOK runs next on a success, and stops the unit with f otherwise.
func (st *step) onOK(f coresvc.Result, next func()) {
	//: the step succeeded.
	if f == coresvc.ResultSuccess {
		next()
		return
	}
	st.enterSignal(coresvc.SubStopSigterm, f)
}

// reloadPostDone ends a reload once ExecReloadPost= ended: its failure fails
// the reload, not the unit.
func (st *step) reloadPostDone(f coresvc.Result) {
	//: a post-reload failure.
	if f != coresvc.ResultSuccess {
		st.m.reloadResult = f
	}
	st.enterRunning(coresvc.ResultSuccess)
}
