package service

import (
	"context"
	"os"
	"slices"
	"sync"

	coresvc "github.com/kitsunium/sdk/internal/core/proc/service"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// inboxSize is how many events wait for the actor before a sender waits.
	inboxSize int = 64
	// maxRecvFailures is how many failed Recv in a row end the notification
	// reader: a source that fails forever is not polled forever.
	maxRecvFailures int = 64
)

// Unit is one service unit, driven by one actor over the pure reducer
// (ADR 0214 §3). Build one with [NewUnit]. Its methods are safe for concurrent
// use; Close ends it.
type Unit struct {
	// ctx is the unit's own context — never a caller's: every spawn and the
	// notification reader run on it, so a stop escalates on its own deadlines
	// (ADR 0214 §7). Close cancels it once the actor ended.
	ctx    context.Context
	cancel context.CancelFunc
	cfg    *Config
	s      *settings
	clock  coresvc.Clock
	spawn  coresvc.Spawner
	// inbox carries every event to the actor; done closes when it ended.
	inbox chan event
	done  chan struct{}
	// waiters are the callers waiting for a job, by id. Guarded by mu.
	waiters map[uint64]chan error
	// subs are the subscriptions. Guarded by mu.
	subs map[*subscription]struct{}
	// snap is the last snapshot published. Guarded by mu.
	snap coresvc.StateValue
	// stored is the store's listing, newest first. Guarded by mu.
	stored []coresvc.StoredFDValue
	// conn is an instance's connection (S0f), which the unit owns and closes
	// at its end; nil for any other unit.
	conn *os.File
	// restoring is what Resume's actor starts from (S0h); frozen what
	// Freeze's actor left. Guarded by mu once the actor runs.
	restoring *restoring
	frozen    *frozenUnit
	// readers joins the notification reader and the actor's end.
	readers sync.WaitGroup
	// doneOnce closes done.
	doneOnce sync.Once
	// dropped counts the events the subscriptions dropped; nextID numbers
	// the waiters. Guarded by mu.
	dropped, nextID uint64
	mu              sync.Mutex
	// closed is set once Close was called; ended once the unit's end is
	// complete, its subscriptions closed. Guarded by mu.
	closed, ended bool
}

// NewUnit checks cfg, resolves its defaults and starts the unit's actor. The
// unit is inactive: NewUnit spawns nothing, Start does. A configuration the
// engine refuses is CONFIG_INVALID, DIRECTIVE_UNSUPPORTED or
// PRIVILEGE_REFUSED, the directive named in the error's fields. cfg is copied;
// the unit owns cfg.Notify from here on, refused or not.
//
// Goroutine lifecycle: the actor, and the notification reader when cfg.Notify
// is set, run until Close; Close joins them.
func NewUnit(cfg *Config) (*Unit, error) {
	own := *cfg
	s, err := resolve(&own)
	//: a configuration the engine refuses.
	if err != nil {
		//: the notification socket is the unit's, even refused.
		if own.Notify != nil {
			gone(own.Notify.Close())
		}
		//: the refusal, naming the directive.
		return nil, err
	}
	//: the unit, inactive.
	return launch(&own, s, nil), nil
}

// launch builds a unit over resolved settings — an instance's (S0f) with the
// connection it owns — and starts its goroutines.
//
// Goroutine lifecycle: the actor and the notification reader, as NewUnit's.
func launch(cfg *Config, s *settings, conn *os.File) *Unit {
	s.put(setInstance, conn != nil)
	u := newUnit(cfg, s)
	u.conn = conn
	u.readers.Add(2)
	//: Goroutine lifecycle: one actor per unit, ended by Close (efExit),
	//: observed through done, joined by Close through readers once its end
	//: — the store and the socket closed — is complete.
	go u.run(s)
	//: the notification reader, when the unit has a socket.
	if cfg.Notify != nil {
		//: Goroutine lifecycle: ended by Close, which cancels ctx and closes
		//: the source; joined by Close through readers.
		go u.readNotify(cfg.Notify)
	} else {
		u.readers.Done()
	}
	//: the unit, inactive.
	return u
}

// newUnit builds the unit's shell.
func newUnit(cfg *Config, s *settings) *Unit {
	ctx, cancel := context.WithCancel(context.Background())
	clk := cfg.Clock
	//: the system clock by default.
	if clk == nil {
		clk = clock.System
	}
	spawner := cfg.Spawner
	//: proc/exec by default.
	if spawner == nil {
		spawner = execSpawner{}
	}
	//: the shell around the actor.
	return &Unit{
		ctx: ctx, cancel: cancel, cfg: cfg, s: s, clock: clk, spawn: spawner,
		inbox: make(chan event, inboxSize), done: make(chan struct{}),
		waiters: map[uint64]chan error{}, subs: map[*subscription]struct{}{},
		snap: coresvc.StateValue{Name: s.name},
	}
}

// Start starts the unit, as systemctl start does, and returns once the start
// job is done: nil when the unit is up — or, for a oneshot, ran to its end, or
// its condition skipped it —, START_FAILED (its result in the fields) when it
// failed or is restarting after a failure, START_LIMIT_HIT when the limiter
// refused it, JOB_CANCELED when a stop replaced it. A unit up already returns
// nil. When ctx ends first Start returns its error and the job goes on.
func (u *Unit) Start(ctx context.Context) error {
	//: the start job.
	return u.request(ctx, evStart)
}

// Stop stops the unit, as systemctl stop does — ExecStop=, the signals,
// ExecStopPost= —, cancels a start running, and returns once the unit is dead.
// The escalation runs on the unit's own deadlines: when ctx ends first Stop
// returns its error and the stop goes on (ADR 0214 §7).
func (u *Unit) Stop(ctx context.Context) error {
	//: the stop job.
	return u.request(ctx, evStop)
}

// Restart stops the unit with RestartKillSignal=, then starts it, and returns
// as Start does. A dead unit is started. The start counts against the limiter.
func (u *Unit) Restart(ctx context.Context) error {
	//: the restart job.
	return u.request(ctx, evRestart)
}

// Reload reloads an up unit — ExecReload=, the notify-reload protocol,
// ExecReloadPost= — and returns once it ran: nil, RELOAD_FAILED with the
// result, RELOAD_UNSUPPORTED when the unit has no mechanism, RELOAD_FAILED
// naming the state when the unit is not up.
func (u *Unit) Reload(ctx context.Context) error {
	//: the reload job.
	return u.request(ctx, evReload)
}

// Fail is a failed health check: WATCHDOG=trigger from outside. The unit is
// aborted with WatchdogSignal= and its run ends with the result watchdog, so
// Restart= decides as after a missed keep-alive. It returns once the abort
// began; a unit not running ignores it.
func (u *Unit) Fail(ctx context.Context) error {
	//: the abort.
	return u.request(ctx, evFail)
}

// ResetFailed is systemctl reset-failed: a failed or skipped unit is inactive
// again, its restart counter and its start limiter are flushed.
func (u *Unit) ResetFailed(ctx context.Context) error {
	//: the reset.
	return u.request(ctx, evResetFailed)
}

// State returns the unit's current snapshot: the level a consumer
// resynchronises on after a gap in its events (ADR 0214 §4).
func (u *Unit) State() coresvc.StateValue {
	u.mu.Lock()
	defer u.mu.Unlock()
	snap := u.snap
	snap.Dropped = u.dropped
	//: a copy.
	return snap
}

// FDStore lists the unit's descriptor store in the order the next ExecStart=
// receives it — newest first, as systemd hands it —, each by its name and
// what it refers to; the descriptors stay the unit's.
func (u *Unit) FDStore() []coresvc.StoredFDValue {
	u.mu.Lock()
	defer u.mu.Unlock()
	//: a copy.
	return slices.Clone(u.stored)
}

// Close ends the unit: the jobs canceled, the unit stopped as Stop does —
// unless SurviveFinalKillSignal=, which leaves its processes running —, then
// the actor ended, the notification socket closed and every subscription's
// channel closed. When ctx ends first Close returns its error and the stop
// goes on; a later Close waits again. Every call after Close is UNIT_CLOSED.
func (u *Unit) Close(ctx context.Context) error {
	u.mu.Lock()
	u.closed = true
	u.mu.Unlock()
	err := u.send(ctx, evClose)
	//: the actor ended before the close was taken: closed already.
	if err != nil && errs.HasCode(err, coresvc.CodeUnitClosed) {
		err = nil
	}
	//: the caller gave up first.
	if err != nil {
		//: the stop goes on.
		return err
	}
	u.readers.Wait()
	//: closed.
	return nil
}

// request sends a caller's request, refused once the unit is closed.
func (u *Unit) request(ctx context.Context, kind eventKind) error {
	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()
	//: a closed unit takes no request.
	if closed {
		//: UNIT_CLOSED.
		return errs.Wrap(coresvc.UnitClosed, errs.WrapParams{}, errs.String("unit", u.s.name))
	}
	//: the request's answer.
	return u.send(ctx, kind)
}

// send posts a request and waits for its answer, within ctx.
func (u *Unit) send(ctx context.Context, kind eventKind) error {
	u.mu.Lock()
	u.nextID++
	id := u.nextID
	answer := make(chan error, 1)
	u.waiters[id] = answer
	u.mu.Unlock()
	defer u.forgetWaiter(id)
	//: posted, or the unit ended, or the caller gave up.
	select {
	//: posted.
	case u.inbox <- event{kind: kind, waiter: id}:
	//: the actor ended.
	case <-u.done:
		//: UNIT_CLOSED.
		return errs.Wrap(coresvc.UnitClosed, errs.WrapParams{}, errs.String("unit", u.s.name))
	//: the caller gave up.
	case <-ctx.Done():
		//: the caller's error.
		return ctx.Err()
	}
	//: answered, or the unit ended, or the caller gave up.
	select {
	//: the job's answer.
	case err := <-answer:
		//: as the reducer gave it.
		return err
	//: the actor ended without answering: a close's waiter is answered by
	//: the end itself.
	case <-u.done:
		//: a close is answered by the end; anything else was canceled by it.
		return u.endAnswer(kind, answer)
	//: the caller gave up; the job goes on.
	case <-ctx.Done():
		//: the caller's error.
		return ctx.Err()
	}
}

// endAnswer is the answer of a request the actor's end overtook.
func (u *Unit) endAnswer(kind eventKind, answer chan error) error {
	//: the answer may have raced the end.
	select {
	//: it did.
	case err := <-answer:
		//: as given.
		return err
	//: it did not.
	default:
	}
	//: the end answers a close.
	if kind == evClose {
		//: closed.
		return nil
	}
	//: UNIT_CLOSED.
	return errs.Wrap(coresvc.UnitClosed, errs.WrapParams{}, errs.String("unit", u.s.name))
}

// forgetWaiter drops a waiter, answered or not.
func (u *Unit) forgetWaiter(id uint64) {
	u.mu.Lock()
	delete(u.waiters, id)
	u.mu.Unlock()
}

// answer sends err to the waiters still waiting.
func (u *Unit) answer(ids []uint64, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	//: each waiter.
	for _, id := range ids {
		//: one that still waits.
		if ch, ok := u.waiters[id]; ok {
			ch <- err
			delete(u.waiters, id)
		}
	}
}

// post hands an event to the actor, or drops it once the actor ended.
func (u *Unit) post(ev *event) bool {
	//: posted, or the actor ended.
	select {
	//: posted.
	case u.inbox <- *ev:
		//: taken.
		return true
	//: nobody to take it.
	case <-u.done:
		//: dropped.
		return false
	}
}

// readNotify reads the unit's notifications until Close.
func (u *Unit) readNotify(src coresvc.NotifySource) {
	defer u.readers.Done()
	failures := 0
	//: one message at a time.
	for failures < maxRecvFailures {
		msg, err := src.Recv(u.ctx)
		//: the unit ended, or the source failed.
		if err != nil {
			//: ended.
			if u.ctx.Err() != nil {
				return
			}
			failures++
			continue
		}
		failures = 0
		//: the actor ended: the descriptors are nobody's.
		if !u.post(&event{kind: evNotify, msg: msg}) {
			closeFiles(msg.Files)
			return
		}
	}
}
