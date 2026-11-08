package service

import (
	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

var (
	// spawnFailures are what a step does when its command cannot be spawned
	// (service.c's "fail:" paths), by list; first is the step's first command,
	// r the result the failure ends the run with (spawnResult).
	spawnFailures = [listCount]func(st *step, first bool, r coresvc.Result){
		listCondition:  (*step).preStartSpawnFailed,
		listStartPre:   (*step).preStartSpawnFailed,
		listStart:      func(st *step, first bool, r coresvc.Result) { st.firstOr(first, st.signalWith(r), st.stopWith(r)) },
		listStartPost:  func(st *step, first bool, r coresvc.Result) { st.firstOr(first, st.stopWith(r), st.signalWith(r)) },
		listReload:     func(st *step, _ bool, r coresvc.Result) { st.reloadSpawnFailed(r) },
		listReloadPost: func(st *step, _ bool, r coresvc.Result) { st.reloadSpawnFailed(r) },
		listStop:       func(st *step, _ bool, r coresvc.Result) { st.signalWith(r)() },
		listStopPost: func(st *step, first bool, r coresvc.Result) {
			st.firstOr(first, func() { st.enterSignal(coresvc.SubFinalSigterm, r) }, func() { st.enterDead(r, true) })
		},
	}
	// sandboxCodes are the sandbox's failures a start reports by their code
	// (S2): a step that failed in the child, a part the kernel lacks.
	sandboxCodes = [...]errs.Code{coreproc.CodeSandboxFailed, coreproc.CodeSandboxUnavailable}
)

// onSpawned records a spawned process, or takes its spawn's failure.
func (st *step) onSpawned() {
	m := st.m
	ev := st.ev
	//: the preparation looked the bus name up: the level before the main
	//: process runs, no readiness (S0e).
	if ev.has(flagLookup) {
		m.set(flagBusOwned, ev.has(flagOwner))
	}
	//: the spawn failed: resources, or exit-code for a sandbox step.
	if ev.spawnErr != nil {
		st.noteSandboxFailure(ev.spawnErr)
		//: a list the table names.
		if ev.list > listNone && ev.list < listCount {
			spawnFailures[ev.list](st, m.cmd == 0, spawnResult(ev.spawnErr))
		}
		return
	}
	//: a control process — a forking unit's ExecStart= among them.
	if ev.list != listStart || m.s.typ == coresvc.TypeForking {
		m.controlPID = ev.pid
		return
	}
	m.mainPID = ev.pid
	m.set(flagMainExited, false)
	//: simple, exec and idle are up once spawned (the spawner returns after
	//: execve succeeded).
	if m.sub == coresvc.SubStart && upOnSpawn(m.s.typ) {
		st.enterStartPost()
	}
}

// upOnSpawn reports a type that is up once its main process is spawned.
func upOnSpawn(t coresvc.Type) bool {
	//: simple, exec, idle.
	return t == coresvc.TypeSimple || t == coresvc.TypeExec || t == coresvc.TypeIdle
}

// firstOr runs first for a step's first command, then for a later one.
func (st *step) firstOr(isFirst bool, first, then func()) {
	//: the step's first command.
	if isFirst {
		first()
		return
	}
	then()
}

// preStartSpawnFailed is a condition or pre-start command that could not be
// spawned: dead at once on the first, signalled on a later one.
func (st *step) preStartSpawnFailed(first bool, r coresvc.Result) {
	st.firstOr(first, func() { st.enterDead(r, true) }, st.signalWith(r))
}

// reloadSpawnFailed fails the reload with r; the unit runs on.
func (st *step) reloadSpawnFailed(r coresvc.Result) {
	st.m.reloadResult = r
	st.enterRunning(coresvc.ResultSuccess)
}

// signalWith signals what runs, with the result r.
func (st *step) signalWith(r coresvc.Result) func() {
	//: deferred to the branch that takes it.
	return func() { st.enterSignal(coresvc.SubStopSigterm, r) }
}

// stopWith stops the unit, with the result r.
func (st *step) stopWith(r coresvc.Result) func() {
	//: deferred to the branch that takes it.
	return func() { st.enterStop(r) }
}

// spawnResult is the result a spawn's failure ends a run with: exit-code for
// a sandbox step that failed in the child before its program ran — what
// systemd's EXIT_* statuses (226/NAMESPACE, 228/SECCOMP, …) are —, resources
// for everything refused or failed before the fork, a part of the sandbox
// the kernel lacks included, as service_spawn's failures are.
func spawnResult(err error) coresvc.Result {
	//: a step of the sandbox, in the child.
	if errs.HasCode(err, coreproc.CodeSandboxFailed) {
		//: the child's exit.
		return coresvc.ResultExitCode
	}
	//: the manager's side.
	return coresvc.ResultResources
}

// noteSandboxFailure keeps the run's first sandbox failure — its code and
// its fields, plain data — for the start's answer (startFailed).
func (st *step) noteSandboxFailure(err error) {
	m := st.m
	//: the first failure wins, as the result does.
	if m.sandboxCause != 0 {
		return
	}
	//: each sandbox code.
	for _, c := range sandboxCodes {
		//: this one.
		if errs.HasCode(err, c) {
			m.sandboxCause, m.sandboxFields = c, errs.FieldsOf(err)
			return
		}
	}
}

// onAttached takes an adoption's outcome: a PID file's (S0c), a guess's
// (S0c), or a MAINPID='s — and what the cgroup listed with it.
func (st *step) onAttached() {
	st.population()
	//: by what asked for it.
	switch {
	//: PIDFile=.
	case st.ev.has(flagByFile):
		st.pidFileRead()
	//: GuessMainPID=.
	case st.ev.has(flagByGuess):
		st.mainGuessed()
	//: MAINPID=.
	default:
		st.mainPIDAttached()
	}
}

// mainPIDAttached takes a MAINPID= adoption: the new main process, or a
// refusal, counted.
func (st *step) mainPIDAttached() {
	//: refused by the attacher.
	if !st.ev.has(flagOK) {
		st.m.notifyRefused++
		return
	}
	st.adoptMain(st.ev.pid)
}
