// Package group runs a set of tasks concurrently and gives their caller one
// place to wait: one first error, one bounded degree of parallelism, and — the
// part the familiar shape leaves out — the panic that a child goroutine would
// otherwise take the whole process down with. It is a kernel primitive
// (stdlib-only, fully domain-neutral: Group, Go, Wait, Collect; no Job, no
// Task, no Worker appears in a signature).
//
// # A panic in a child is delivered, not fatal
//
// A goroutine that panics and is not recovered ON ITS OWN STACK kills the
// process. No amount of recover() in the parent helps, which is why the common
// "spawn N goroutines, collect errors" helper turns one bad input into a crash
// with a stack pointing at a goroutine the reader has never seen. Here the
// recovery happens inside the child, together with the stack of the goroutine
// that actually failed, and is re-raised in [Group.Wait] as a [PanicValue].
//
// The re-raise happens AFTER every other task has returned, so the structured
// guarantee survives the fault: when Wait leaves — by return or by panic — the
// group owns no running goroutine.
//
// # What Wait guarantees, and what it cannot
//
// Wait returns when every task started by [Group.Go] has RETURNED. It does not
// return when the context is cancelled, because cancellation is a request and
// Go has no way to force a goroutine to honour one.
//
// So a task that ignores its context keeps Wait blocked for as long as it
// runs, and that is the contract rather than a defect: the alternative —
// returning while goroutines are still live — is precisely the leak structured
// concurrency exists to prevent, and it would hand the caller a "finished"
// group that is still writing to their memory. Bound such a task from the
// inside (a deadline on the I/O it performs). A group cannot bound it from the
// outside without abandoning it.
//
// # Limits
//
// The concurrency limit is a real bound taken before the goroutine is created,
// so N submissions against a limit of L hold L goroutines, not N. A
// non-positive limit is clamped to 1 and [Unlimited] must be spelled out —
// see [New].
package group

import (
	"context"
	"math"
	"sync"
)

// Unlimited is the concurrency limit that bounds nothing: a slot is always
// free, so [Group.Go] never waits for one.
//
// It has to be written out. A zero that silently meant "no limit" would be a
// value the caller never chose doing something they never asked for, and a
// zero that meant "no task may run" is the deadlock ADR 0031 exists to remove
// from this SDK — errgroup's SetLimit(0) parks every Go call forever. Naming
// the case leaves neither reading available to a typo.
const Unlimited int = math.MaxInt

// Group runs tasks concurrently under one context and one wait point.
//
// The zero value is NOT usable — construct with [New], which hands back the
// context the tasks receive alongside it. A Group is used once: after [Wait]
// has returned, submit to a new one.
//
// [Go] may be called from any goroutine. [Wait] may not run concurrently with
// [Go] — "have all submissions been made" is a question only the submitter can
// answer, and a Group that guessed would report a total that was true for an
// instant.
type Group struct {
	// ctx is handed to every task. It is stored rather than closed over so a
	// task signature can name it, which is what stops a caller from wiring the
	// wrong context into a goroutine they did not write.
	ctx context.Context
	// cancel releases ctx. It carries a cause so a sibling can tell "another
	// task failed, with this error" from "the parent went away".
	cancel context.CancelCauseFunc
	// sem is the concurrency bound. A token is taken in Go, before the
	// goroutine exists, and returned when the task ends.
	sem chan struct{}
	// wg reaches zero exactly when every started task has returned.
	wg sync.WaitGroup

	// mu guards the two outcome fields below, which any task may write and
	// Wait reads.
	mu sync.Mutex
	// err is the first non-nil error a task returned.
	err error
	// panicked is the first panic a task raised, with its originating stack.
	panicked *PanicValue
}

// New returns a Group and the context every task submitted to it receives.
//
// limit bounds how many tasks run at once. A non-positive limit is CLAMPED to
// 1 — one task at a time, slow but correct — and is read neither as "no limit"
// nor as "no task may run"; pass [Unlimited] for the former, and note that the
// latter is what makes a zero limit a deadlock elsewhere. The clamp follows the
// SDK's own bulkhead precedent (ADR 0031 §"Why not extend this to the other
// clamps"): a concurrency floor of one still runs the caller's work under a
// meaningful, if minimal, policy, so it is a floor rather than a guess at
// intent.
//
// The returned context is cancelled when the first task fails, when a task
// panics, or when [Group.Wait] returns — whichever happens first. On a task
// failure that task's error is the context's [context.Cause].
func New(parent context.Context, limit int) (*Group, context.Context) {
	//: a limit that admits nothing is not a policy, it is a deadlock; one is
	//: the floor below which there is nothing left to run.
	if limit < 1 {
		//: serial execution — minimal, but it is execution.
		limit = 1
	}
	ctx, cancel := context.WithCancelCause(parent)
	//: a struct{} element makes the buffer free at any size, so Unlimited
	//: costs a channel header and no memory — see
	//: TestUnlimitedSemaphoreIsAllocatedNotApproximated.
	runner := &Group{ctx: ctx, cancel: cancel, sem: make(chan struct{}, limit)}
	//: the caller needs the context to derive deadlines and to pass on.
	return runner, ctx
}

// Go starts fn on a goroutine of its own, blocking while the group already has
// its limit of tasks running.
//
// fn receives the group's context. A task submitted after the group has
// already failed still RUNS, with an already-cancelled context that a
// cooperative task returns from at once. Skipping it silently would be the
// worse half of the trade — a task that never ran and never said so.
//
// Go must not be called after [Group.Wait].
func (g *Group) Go(fn func(ctx context.Context) error) {
	//: take the slot HERE, on the submitting goroutine, so N submissions
	//: against a limit of L hold L goroutines rather than N parked ones.
	g.sem <- struct{}{}
	//: registered before the goroutine starts, so Wait cannot observe zero
	//: between the submission and the first line of the task.
	g.wg.Add(1)
	go g.run(fn)
}

// Wait blocks until every task started by [Group.Go] has returned, releases the
// group context, and returns the first error any task reported.
//
// If a task panicked, Wait re-raises it as a [PanicValue] carrying the stack of
// the goroutine that failed — after every other task has finished, so no task
// outlives Wait on the panic path either. A panic outranks an error: an error
// is an outcome the caller can handle, a panic says the result is not one.
//
// Wait does NOT return early on cancellation. See the package documentation
// for what that guarantees and what it costs.
func (g *Group) Wait() error {
	//: the structured guarantee: no goroutine of this group outlives this line.
	g.wg.Wait()
	//: release the derived context BEFORE the possible re-raise below, so a
	//: panicking group still frees its context and its parent's child slot.
	g.cancel(nil)
	g.mu.Lock()
	panicked, err := g.panicked, g.err
	g.mu.Unlock()
	//: a fault is not an outcome; it is re-raised rather than reported.
	if panicked != nil {
		//: the value carries fn's own stack — see PanicValue.
		panic(*panicked)
	}
	//: the first failure, or nil when every task succeeded.
	return err
}

// run executes fn and releases the group's hold on its goroutine. It exists as
// a method rather than a closure so the defer ordering below is reviewable in
// one place — it is what makes a panic recoverable AND the slot returned.
func (g *Group) run(fn func(ctx context.Context) error) {
	//: registered first, so it runs LAST: the slot is freed and the wait group
	//: drained only after the panic has been captured, which is what lets Wait
	//: see the capture instead of racing it.
	defer func() {
		<-g.sem
		g.wg.Done()
	}()
	//: recover() only reports a panic when it is called directly by a deferred
	//: function of the panicking frame, so this cannot be folded into the
	//: defer above.
	defer g.capturePanic()
	//: the caller's work, under the group's context.
	if err := fn(g.ctx); err != nil {
		//: the first failure stops the siblings.
		g.fail(err)
	}
}

// fail records the first error and cancels the group so its siblings can stop.
//
// Later errors are dropped on purpose: the group reports the failure that
// STARTED the shutdown, and almost every error after it is a consequence of the
// cancellation the first one caused. Reporting a cascade would bury the cause.
//
// Only the call that records the first error cancels. Recording and cancelling
// are two steps, and the first cancel is the one whose cause sticks, so if
// every failing task cancelled, a task that lost the race to record could win
// the race to cancel — and context.Cause would name a different error than
// Wait returns. TestWaitAndTheContextCauseNameTheSameFailure is the guard.
func (g *Group) fail(err error) {
	g.mu.Lock()
	first := g.err == nil
	//: first writer wins; the rest are the wake of this one.
	if first {
		g.err = err
	}
	g.mu.Unlock()
	//: a later failure has nothing to add: the group is already stopping, or
	//: is about to, with the cause Wait will report.
	if !first {
		//: only the recording call cancels, so the cause cannot be overtaken.
		return
	}
	//: cancel with the cause so a sibling reading context.Cause learns WHY it
	//: is being stopped — the same error Wait returns.
	g.cancel(err)
}

// capturePanic recovers a task's panic on the task's own goroutine — the only
// place recover() can see it — and turns it into a value Wait can re-raise.
func (g *Group) capturePanic() {
	//: nil on the normal path, which is the overwhelming case.
	raised := recover()
	if raised == nil {
		//: fn returned; nothing to carry.
		return
	}
	g.mu.Lock()
	//: the first panic is the one re-raised; a later one is almost always a
	//: consequence of the cancellation this one is about to trigger.
	if g.panicked == nil {
		//: capture the stack HERE, while the failing goroutine is unwinding —
		//: a stack taken in Wait would point at code that did nothing wrong.
		g.panicked = newPanicValue(raised)
	}
	g.mu.Unlock()
	//: a panicking task is a failing task: stop the siblings. The cause stays
	//: context.Canceled because a PanicValue is deliberately not an error.
	g.cancel(nil)
}
