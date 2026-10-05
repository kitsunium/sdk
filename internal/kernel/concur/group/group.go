package group

import (
	"context"
	"errors"
	"math"
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

// NewJoined returns a Group whose [Group.Wait] reports EVERY task's error,
// joined with errors.Join in the order the tasks were submitted, rather than
// the first one. Everything else is [New]'s: the limit and its clamp, the
// returned context, the siblings cancelled on the first failure — whose error
// is still the context's [context.Cause] — and a panic re-raised in Wait,
// which still outranks every error.
//
// It is for tasks whose failures are independent facts: N workers polling one
// broken medium each report their own failure, and dropping all but one would
// hide which of them saw what. When later failures are mostly the echo of the
// cancellation the first one caused, [New] is the right group.
//
// The joined error is matchable as each of its parts — errors.Is, errors.As,
// and the SDK's errs.HasCode all walk Unwrap() []error — and Wait returns nil,
// not an empty join, when no task failed.
func NewJoined(parent context.Context, limit int) (*Group, context.Context) {
	runner, ctx := New(parent, limit)
	//: set before the group is handed out, so no submission can race it.
	runner.failures = new([]error)
	//: the same context New derived.
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
	//: a joined group claims the task's failure slot HERE, on the submitting
	//: goroutine, so the join keeps submission order whatever order the
	//: tasks fail in.
	if g.failures != nil {
		go g.runJoined(fn, g.reserve())
		//: submitted.
		return
	}
	go g.run(fn)
}

// reserve claims the next slot of a joined group's failure list and returns
// its index: the task's position among the submissions.
func (g *Group) reserve() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	//: one nil slot per submission, filled only if that task fails.
	*g.failures = append(*g.failures, nil)
	//: its index.
	return len(*g.failures) - 1
}

// runJoined runs fn as run does, keeping its error in the failure slot the
// submission reserved. It is a separate path so a first-error group's
// goroutine carries no slot it would never use.
func (g *Group) runJoined(fn func(ctx context.Context) error, slot int) {
	//: the same run — and so the same panic capture — around a task that
	//: files its own failure before reporting it.
	g.run(func(ctx context.Context) error {
		err := fn(ctx)
		//: a success leaves its slot nil, which errors.Join drops.
		if err != nil {
			g.record(slot, err)
		}
		//: run still cancels the siblings on the first failure.
		return err
	})
}

// Wait blocks until every task started by [Group.Go] has returned, releases the
// group context, and returns the first error any task reported — or, for a
// group from [NewJoined], every one of them, joined in submission order.
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
	//: a joined group reports every failure, in submission order; Join drops
	//: the nil slots and returns nil when every one is nil.
	if g.failures != nil {
		//: no task can write a slot any more: they have all returned.
		return errors.Join(*g.failures...)
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

// record stores err in a joined group's failure list at slot.
func (g *Group) record(slot int, err error) {
	g.mu.Lock()
	//: each task owns its slot; the lock orders the write before Wait's read,
	//: and keeps it apart from a reserve growing the list.
	(*g.failures)[slot] = err
	g.mu.Unlock()
}

// fail records the first error and cancels the group so its siblings can stop.
//
// In a first-error group later errors are dropped on purpose: the group
// reports the failure that STARTED the shutdown, and almost every error after
// it is a consequence of the cancellation the first one caused. Reporting a
// cascade would bury the cause. A joined group has already kept each of them
// in its failure list (record); here it only decides the cause, like any other.
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
