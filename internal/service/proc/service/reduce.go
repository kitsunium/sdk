package service

import (
	"errors"
	"slices"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

var (
	// handlers are the reducer's branch for each kind of event.
	handlers = [...]func(*step){
		evStart:       func(st *step) { st.onStart(jobStart) },
		evStop:        (*step).onStop,
		evRestart:     func(st *step) { st.onStart(jobRestart) },
		evReload:      (*step).onReload,
		evFail:        (*step).onFail,
		evResetFailed: (*step).onResetFailed,
		evClose:       (*step).onClose,
		evSpawned:     (*step).onSpawned,
		evExit:        (*step).onExit,
		evTimer:       (*step).onTimer,
		evNotify:      (*step).onNotify,
		evAttached:    (*step).onAttached,
		evHangup:      (*step).onHangup,
		evCgroup:      (*step).onCgroup,
		evSignaled:    (*step).onSignaled,
		evBusName:     (*step).onBusName,
	}
	// settlers complete the job of each kind the unit's state answers.
	settlers = [...]func(*step){
		jobNone:    func(*step) {},
		jobStart:   (*step).settleStart,
		jobStop:    (*step).settleStop,
		jobRestart: (*step).settleRestart,
		jobReload:  (*step).settleReload,
	}
)

// reduce is the unit's pure reducer (ADR 0214 §3): it moves the model by ev,
// in place, and returns the effects the shell must run. It reads nothing but
// its arguments.
func reduce(m *model, ev *event) []effect {
	st := &step{m: m, ev: ev}
	//: a kind the table names.
	if int(ev.kind) < len(handlers) {
		handlers[ev.kind](st)
	}
	st.settle()
	st.publish()
	//: what the shell must do, the answers last: a caller its job answered
	//: reads the snapshot and the store the job left.
	return answersLast(st.out)
}

// answersLast moves the answers to callers — efReply, efExit — after every
// other effect of a step, in their order.
func answersLast(out []effect) []effect {
	isAnswer := func(ef effect) bool { return ef.kind == efReply || ef.kind == efExit }
	//: no answer, or nothing after one.
	if !slices.ContainsFunc(out, isAnswer) {
		//: as emitted.
		return out
	}
	sorted := make([]effect, 0, len(out))
	//: the effects, then the answers.
	for _, answers := range []bool{false, true} {
		//: each effect of the pass.
		for i := range out {
			//: in this pass.
			if isAnswer(out[i]) == answers {
				sorted = append(sorted, out[i])
			}
		}
	}
	//: reordered.
	return sorted
}

// emit appends one effect.
func (st *step) emit(ef *effect) {
	st.out = append(st.out, *ef)
}

// reply answers waiters with err.
func (st *step) reply(waiters []uint64, err error) {
	//: nobody to answer.
	if len(waiters) == 0 {
		return
	}
	st.emit(&effect{kind: efReply, waiters: waiters, err: err})
}

// refusal is a sentinel naming the unit.
func (st *step) refusal(sentinel *errs.Error) error {
	//: the sentinel with the unit's name.
	return errs.Wrap(sentinel, errs.WrapParams{}, errs.String("unit", st.m.s.name))
}

// publish emits the snapshot when it differs from the last one published.
func (st *step) publish() {
	mdl := st.m
	snap := snapshot(mdl)
	last := mdl.published
	last.Generation, last.At = 0, time.Time{}
	//: nothing a consumer can see changed.
	if snap == last {
		return
	}
	mdl.generation++
	snap.Generation, snap.At = mdl.generation, st.ev.at
	prev := mdl.published
	mdl.published = snap
	st.emit(&effect{kind: efPublish, snap: snap, prevState: prev.State, prevSub: prev.Sub})
}

// snapshot is the model as a consumer reads it, without its generation.
func snapshot(m *model) coresvc.StateValue {
	//: the visible fields.
	return coresvc.StateValue{
		Changed: m.changed, Name: m.s.name, Status: m.status, BusError: m.busError,
		VarlinkError: m.varlinkError, ExitStatusText: m.exitStatusText, MainExit: m.mainExit,
		NotifyRefused: m.notifyRefused, MainPID: m.mainPID, ControlPID: m.controlPID,
		FDStoreRefused: m.storeRefused, FDStoreDiscarded: m.storeDiscarded, FDStore: len(m.store),
		Errno: m.errno, Restarts: m.restarts, State: m.state, Sub: m.sub, Result: m.result,
		ReloadResult: m.reloadResult, NotifyAccess: m.notifyAccess, MainExited: m.has(flagMainExited),
	}
}

// isDead reports whether a state has no process the engine runs.
func isDead(s coresvc.State) bool {
	//: inactive, failed, skipped.
	return s == coresvc.StateInactive || s == coresvc.StateFailed || s == coresvc.StateConditionSkipped
}

// upSub reports whether a step is up and settled: running — a Type=dbus
// unit's revalidation included (S0e) — or exited.
func upSub(sub coresvc.Sub) bool {
	//: running, revalidating, exited.
	return sub == coresvc.SubRunning || sub == coresvc.SubRunningRevalidating || sub == coresvc.SubExited
}

// isUp reports whether a state is up.
func isUp(s coresvc.State) bool {
	//: active, exited, reloading.
	return s == coresvc.StateActive || s == coresvc.StateExited || s == coresvc.StateReloading
}

// joined appends waiters to a copy of a job's.
func joined(j job, w []uint64) []uint64 {
	//: a fresh slice: the model's may be shared with a replayed one.
	return append(slices.Clone(j.waiters), w...)
}

// onStart takes a start, or a restart.
func (st *step) onStart(kind jobKind) {
	w := []uint64{st.ev.waiter}
	//: a closing unit takes no job.
	if st.m.has(flagClosing) {
		st.reply(w, st.refusal(coresvc.UnitClosed))
		return
	}
	//: a job running takes it.
	if st.queueStart(kind, w) {
		return
	}
	st.startNow(kind, w)
}

// queueStart joins or queues a start behind the job running, and reports
// whether it did.
func (st *step) queueStart(kind jobKind, w []uint64) bool {
	m := st.m
	//: by the job running.
	switch m.job.kind {
	//: a start is joined.
	case jobStart:
		m.job.waiters = joined(m.job, w)
	//: a restart's start half is joined, its stop half waited for.
	case jobRestart, jobStop:
		st.queueBehindStop(kind, w)
	//: a reload: a start is up already, a restart replaces it.
	case jobReload:
		//: replaced.
		if kind == jobRestart {
			st.reply(m.job.waiters, st.refusal(coresvc.JobCanceled))
			m.job = job{}
			//: the restart runs now.
			return false
		}
		st.reply(w, nil)
	//: no job.
	default:
		//: the start runs now.
		return false
	}
	//: taken by the job running.
	return true
}

// queueBehindStop joins a restart's start half, or waits for a stop.
func (st *step) queueBehindStop(kind jobKind, w []uint64) {
	m := st.m
	//: the start half runs: joined.
	if m.job.kind == jobRestart && m.job.stopped {
		m.job.waiters = joined(m.job, w)
		return
	}
	m.pending = job{kind: kind, waiters: joined(m.pending, w)}
}

// startNow runs a start or a restart with no job running.
func (st *step) startNow(kind jobKind, w []uint64) {
	m := st.m
	//: where the unit stands.
	switch {
	//: up already.
	case kind == jobStart && isUp(m.state):
		st.reply(w, nil)
	//: starting on its own (an automatic restart's activation): joined.
	case st.activatingAlone(kind):
		m.job = job{kind: jobStart, waiters: w, stopped: true}
	//: stopping on its own: the start waits for the end.
	case m.state == coresvc.StateDeactivating:
		m.job = job{kind: jobStop}
		m.pending = job{kind: kind, waiters: w}
	//: a restart of a unit that runs stops it first.
	case st.restartsRunning(kind):
		m.job = job{kind: jobRestart, waiters: w}
		m.set(flagForbidRestart, true)
		m.set(flagRestarting, true)
		st.stopNow()
	//: dead, failed, skipped or waiting to restart: start now.
	default:
		m.job = job{kind: kind, waiters: w, stopped: true}
		st.beginStart(true)
	}
}

// activatingAlone reports a start asked of a unit starting with no job.
func (st *step) activatingAlone(kind jobKind) bool {
	//: a start, while activating.
	return kind == jobStart && st.m.state == coresvc.StateActivating
}

// restartsRunning reports a restart asked of a unit that runs.
func (st *step) restartsRunning(kind jobKind) bool {
	s := st.m.state
	//: neither dead nor waiting to restart.
	return kind == jobRestart && !isDead(s) && s != coresvc.StateAutoRestart
}

// onStop takes a stop.
func (st *step) onStop() {
	m := st.m
	w := []uint64{st.ev.waiter}
	//: a stop running is joined.
	if m.job.kind == jobStop {
		m.job.waiters = joined(m.job, w)
		return
	}
	//: a start, restart or reload running is canceled.
	if m.job.kind != jobNone {
		st.reply(m.job.waiters, st.refusal(coresvc.JobCanceled))
		m.job = job{}
	}
	st.reply(m.pending.waiters, st.refusal(coresvc.JobCanceled))
	m.pending = job{}
	m.set(flagForbidRestart, true)
	//: dead already.
	if isDead(m.state) {
		st.reply(w, nil)
		return
	}
	m.job = job{kind: jobStop, waiters: w}
	st.stopNow()
}

// stopNow begins a stop from where the unit stands (service.c's service_stop).
func (st *step) stopNow() {
	//: one way down per state.
	switch st.m.state {
	//: waiting to restart: dead at once.
	case coresvc.StateAutoRestart:
		st.disarm(timerState)
		st.setState(coresvc.StateInactive, coresvc.SubDead)
	//: starting: what runs is signalled, ExecStop= is skipped.
	case coresvc.StateActivating:
		st.enterSignal(coresvc.SubStopSigterm, coresvc.ResultSuccess)
	//: up or reloading: ExecStop= first.
	case coresvc.StateActive, coresvc.StateExited, coresvc.StateReloading:
		st.enterStop(coresvc.ResultSuccess)
	//: dead or stopping already.
	default:
	}
}

// onReload takes a reload.
func (st *step) onReload() {
	m := st.m
	w := []uint64{st.ev.waiter}
	//: a reload joins a reload.
	if m.job.kind == jobReload {
		m.job.waiters = joined(m.job, w)
		return
	}
	//: no mechanism: ExecReload= or the notify-reload protocol.
	if len(m.s.lists[listReload]) == 0 && m.s.typ != coresvc.TypeNotifyReload {
		st.reply(w, st.refusal(coresvc.ReloadUnsupported))
		return
	}
	//: only an up unit with no job reloads.
	if m.has(flagClosing) || m.job.kind != jobNone || !upSub(m.sub) {
		st.reply(w, errs.Wrap(coresvc.ReloadFailed, errs.WrapParams{}, errs.String("unit", m.s.name), errs.String("state", m.sub.String())))
		return
	}
	m.job = job{kind: jobReload, waiters: w}
	st.enterReload()
}

// onFail takes a failed health check: WATCHDOG=trigger from outside.
func (st *step) onFail() {
	st.forceWatchdog()
	st.reply([]uint64{st.ev.waiter}, nil)
}

// onResetFailed takes a reset-failed: a failed unit inactive, the restart
// counter and the start limiter flushed.
func (st *step) onResetFailed() {
	m := st.m
	//: failed or skipped is inactive again.
	if m.state == coresvc.StateFailed || m.state == coresvc.StateConditionSkipped {
		st.setState(coresvc.StateInactive, coresvc.SubDead)
		m.result, m.reloadResult = coresvc.ResultSuccess, coresvc.ResultSuccess
	}
	m.restarts, m.limitCount, m.limitBegin = 0, 0, time.Time{}
	st.reply([]uint64{st.ev.waiter}, nil)
}

// onClose takes a close: the jobs canceled, then the unit stopped — or left
// running under SurviveFinalKillSignal= — and the actor ended.
func (st *step) onClose() {
	m := st.m
	m.closeWaiters = append(slices.Clone(m.closeWaiters), st.ev.waiter)
	//: closing already: one more waiter.
	if m.has(flagClosing) {
		return
	}
	m.set(flagClosing, true)
	closed := st.refusal(coresvc.UnitClosed)
	st.reply(m.pending.waiters, closed)
	m.pending = job{}
	//: the job running is canceled, but a stop.
	if m.job.kind != jobNone && m.job.kind != jobStop {
		st.reply(m.job.waiters, closed)
		m.job = job{}
	}
	//: the processes survive the supervisor.
	if m.s.on(setSurviveFinal) {
		st.forgetAll()
		st.exit()
		return
	}
	//: something to stop, and no stop running.
	if !isDead(m.state) && m.job.kind != jobStop {
		m.set(flagForbidRestart, true)
		m.job = job{kind: jobStop}
		st.stopNow()
	}
}

// settle completes the job the unit's state answers, and ends a closing unit
// with nothing left to do.
func (st *step) settle() {
	m := st.m
	settlers[m.job.kind](st)
	st.settleStore()
	//: a closing unit — or an instance whose run is over (S0f) — with nothing
	//: left to do ends.
	if (m.has(flagClosing) || st.instanceOver()) && m.job.kind == jobNone && isDead(m.state) {
		st.exit()
	}
}

// instanceOver reports an instance (S0f) that ran and has no start queued:
// dead, it ends, its connection closed with it.
func (st *step) instanceOver() bool {
	m := st.m
	//: an instance that ran, nothing queued.
	return m.s.on(setInstance) && m.has(flagRan) && m.pending.kind == jobNone
}

// settleRestart begins a restart's start half once its stop half reached a
// dead state, then answers it as a start.
func (st *step) settleRestart() {
	m := st.m
	//: the stop half is over.
	if !m.job.stopped && (isDead(m.state) || m.state == coresvc.StateAutoRestart) {
		m.job.stopped = true
		m.set(flagRestarting, false)
		st.disarm(timerState)
		st.beginStart(true)
	}
	st.settleStart()
}

// settleStart answers a start job once the unit is up, dead, failed or
// waiting to restart.
func (st *step) settleStart() {
	m := st.m
	//: a restart's stop half is still running.
	if !m.job.stopped {
		return
	}
	done, err := st.startOutcome()
	//: still starting.
	if !done {
		return
	}
	st.reply(m.job.waiters, err)
	m.job = job{}
}

// startOutcome reports whether a start is over, and its answer.
func (st *step) startOutcome() (bool, error) {
	m := st.m
	//: where the start ended.
	switch {
	//: the limiter refused it.
	case m.result == coresvc.ResultStartLimitHit && isDead(m.state):
		//: START_LIMIT_HIT.
		return true, st.refusal(coresvc.StartLimitHit)
	//: up, a oneshot that ran to its end, or a skipped condition.
	case st.startedClean():
		//: done.
		return true, nil
	//: failed, or failed and restarting on its own.
	case m.state == coresvc.StateFailed || m.state == coresvc.StateAutoRestart:
		//: START_FAILED with the result.
		return true, st.startFailed()
	//: still starting.
	default:
		//: not yet.
		return false, nil
	}
}

// startFailed is START_FAILED naming the unit, its result and — for a
// Type=forking unit that never read its PID file — why the file was refused.
// A run that failed on its sandbox keeps START_FAILED outside — its code,
// its reason, errors.Is(err, StartFailed) — and the sandbox's failure inside:
// errs.Wrap's origin is the first *Error its cause holds, so the cause is
// START_FAILED joined to the sandbox's failure, which errors.Is,
// errors.AsType and errs.HasCode reach through the join; the step or the
// feature it named is among the fields.
func (st *step) startFailed() error {
	m := st.m
	fields := []errs.FieldValue{errs.String("unit", m.s.name), errs.String("result", m.result.String())}
	//: the PID file's last refusal.
	if m.pidFileCause != "" {
		fields = append(fields, errs.String("pidfile", m.pidFileCause))
	}
	//: no sandbox failure.
	if m.sandboxCause == 0 {
		//: the sentinel with its fields.
		return errs.Wrap(coresvc.StartFailed, errs.WrapParams{}, fields...)
	}
	cause := errs.Wrap(sandboxSentinel(m.sandboxCause), errs.WrapParams{}, m.sandboxFields...)
	//: START_FAILED first, the origin; the sandbox's failure beside it.
	return errs.Wrap(errors.Join(coresvc.StartFailed, cause), errs.WrapParams{}, append(fields, m.sandboxFields...)...)
}

// sandboxSentinel is the sentinel of a sandbox failure's code.
func sandboxSentinel(code errs.Code) error {
	//: a part the kernel lacks.
	if code == coreproc.CodeSandboxUnavailable {
		//: SANDBOX_UNAVAILABLE.
		return coreproc.SandboxUnavailable
	}
	//: a step that failed in the child.
	return coreproc.SandboxFailed
}

// startedClean reports a start that ended without a failure: up, a oneshot
// that ran to its end, or a skipped condition.
func (st *step) startedClean() bool {
	m := st.m
	//: up, or resting without a failure.
	return upSub(m.sub) || m.state == coresvc.StateInactive || m.state == coresvc.StateConditionSkipped
}

// settleStop answers a stop job once the unit is dead, then starts what
// waited for it.
func (st *step) settleStop() {
	m := st.m
	//: still stopping.
	if !isDead(m.state) {
		return
	}
	st.reply(m.job.waiters, nil)
	m.job = job{}
	//: a start waited for the stop.
	if m.pending.kind != jobNone && !m.has(flagClosing) {
		m.job, m.pending = m.pending, job{}
		m.job.stopped = true
		st.beginStart(true)
		st.settleStart()
	}
}

// settleReload answers a reload job once the unit is up again, or cancels it
// once the unit left.
func (st *step) settleReload() {
	m := st.m
	//: up again: the reload's result.
	if upSub(m.sub) {
		var err error
		//: a reload that did not succeed.
		if m.reloadResult != coresvc.ResultSuccess {
			err = errs.Wrap(coresvc.ReloadFailed, errs.WrapParams{}, errs.String("unit", m.s.name), errs.String("result", m.reloadResult.String()))
		}
		st.reply(m.job.waiters, err)
		m.job = job{}
		return
	}
	//: the unit left while reloading.
	if m.state != coresvc.StateReloading {
		st.reply(m.job.waiters, st.refusal(coresvc.JobCanceled))
		m.job = job{}
	}
}

// exit asks the shell to end the actor, once.
func (st *step) exit() {
	m := st.m
	//: asked already.
	if m.has(flagExited) {
		return
	}
	m.set(flagExited, true)
	st.disarm(timerState)
	st.disarm(timerWatchdog)
	st.disarm(timerPIDFile)
	st.emit(&effect{kind: efExit, waiters: m.closeWaiters})
	m.closeWaiters = nil
}
