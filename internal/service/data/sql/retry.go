package sql

import (
	"context"
	"errors"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// sqliteBusy and sqliteLocked are SQLite's two primary result codes that
// mean "another connection holds what this one needs": the low byte of every
// extended form, SQLITE_BUSY_SNAPSHOT (517) among them.
const (
	sqliteBusy   int = 5
	sqliteLocked int = 6
	// sqlitePrimary masks an extended result code down to its primary code.
	sqlitePrimary int = 0xff
)

// DefaultRetryAttempts is how many times a retrying transactor runs a
// transaction when [RetryConfig.Attempts] is not positive: the first attempt
// and two more. A conflict that three attempts did not clear is contention
// the caller should hear about, not one a fourth will.
const DefaultRetryAttempts int = 3

// DefaultRetryDelay is the base of the wait between two attempts when
// [RetryConfig.Backoff] is the zero value, which then grows by 2 per attempt
// with full jitter: two transactions that deadlocked each other and retry
// on the same curve without jitter meet again at the same instant.
const DefaultRetryDelay time.Duration = 25 * time.Millisecond

// RetryConfig parameterises [NewRetrying]: how many times a root transaction
// may run, how long to wait between two runs, which failures earn another
// run, and the clock the waits are read from.
//
// Every field has a documented fallback (ADR 0031's clamp side): retrying a
// transaction the database asked to be run again has no dangerous zero.
type RetryConfig struct {
	// Attempts is how many times a root transaction may run in all, the
	// first included: 1 runs it once and never again. A non-positive value
	// clamps to [DefaultRetryAttempts].
	Attempts int
	// Backoff is the curve of the waits between two attempts: the wait
	// after the n-th failed attempt is Backoff.Delay(n). The zero value
	// clamps to [DefaultRetryDelay] grown by 2 per attempt with full jitter;
	// any other value is the caller's curve, as given — a BaseDelay of zero
	// with a ceiling or a jitter is a caller's choice to retry at once.
	Backoff kbackoff.Value
	// Transient decides which failures earn another attempt. A nil value
	// clamps to [Classify]. A context's end never does, whatever Transient
	// answers.
	Transient coresql.Transient
	// Clock is the time source the waits are read from. A nil Clock falls
	// back to clock.System; a test hands a ManualClock and advances it
	// rather than sleeping.
	Clock clock.Timed
}

// retrying is the core/data/sql.Transactor NewRetrying returns: tm's own
// transactions, run again on a transient failure. Its identity keys the
// context mark a nested call is recognised by.
type retrying struct {
	// inner is the transactor every attempt runs on.
	inner coresql.Transactor
	// joiner is inner's Joiner, nil when it offers none: how a context that
	// carries a transaction inner opened itself is recognised as nested.
	joiner coresql.Joiner
	// attempts is the clamped budget, the first run included.
	attempts int
	// curve is the clamped backoff.
	curve kbackoff.Value
	// transient is the clamped classifier.
	transient coresql.Transient
	// clk is the clock every wait is read from.
	clk clock.Timed
}

// retryingSiblings is a retrying transactor over one that offers the three
// ADR 0139 siblings: each forwards to it, so a consumer that asserts for
// them reaches the transactor the attempts run on.
type retryingSiblings struct {
	*retrying
	coresql.Joiner
	coresql.Deferrer
	coresql.Aborter
}

// retryMark is the context key a root attempt marks its context with: a
// Transact on the same retrying transactor under it is nested.
type retryMark struct {
	// owner is the retrying transactor that opened the root.
	owner *retrying
}

// NewRetrying returns a transactor that runs a ROOT transaction of tm again
// when it fails on an error cfg.Transient calls transient, up to
// cfg.Attempts runs, waiting cfg.Backoff's curve on cfg.Clock between two.
//
// Each attempt is a whole transaction of tm: a new BEGIN, the unit of work
// from its start, and the commit or the rollback tm decides — so what the
// unit of work held with Defer is dropped with each attempt that rolled back
// and runs once, after the commit that stood, and what it held with OnAbort
// runs after each rollback. The unit of work MUST therefore be safe to run
// more than once: its effects outside the transaction belong in Defer, as
// ADR 0055 asks of a retried transaction.
//
// A NESTED call — one whose context already carries a transaction of tm,
// opened through this transactor or through tm itself — joins it as tm
// does, and is never run again on its own: a savepoint cannot be retried
// without the transaction around it. A failure that is not transient, and
// a context that ended, return at once. When the budget is spent the last
// attempt's error is returned joined with [coresql.TxRetriesExhausted],
// whose "attempts" field says how many ran.
//
// The transactor offers [coresql.Joiner], [coresql.Deferrer] and
// [coresql.Aborter] exactly when tm offers all three, forwarding each to tm.
// A nil tm is refused with [coresql.ConfigInvalid].
func NewRetrying(tm coresql.Transactor, cfg RetryConfig) (manager coresql.Transactor, err error) {
	//: nothing to run the attempts on is a wiring fault.
	if tm == nil {
		//: the missing field names which half is absent.
		return nil, kerrs.Wrap(coresql.ConfigInvalid, kerrs.WrapParams{}, kerrs.String("missing", "transactor"))
	}
	r := &retrying{inner: tm, attempts: cfg.attempts(), curve: cfg.curve(), transient: cfg.classifier(), clk: cfg.clock()}
	joiner, joins := tm.(coresql.Joiner)
	deferrer, defers := tm.(coresql.Deferrer)
	aborter, aborts := tm.(coresql.Aborter)
	//: a transactor that is not the SDK's may offer none of the siblings, and
	//: claiming one it lacks would answer a type assertion with a lie.
	if !joins || !defers || !aborts {
		//: Transact alone; a Joiner, if any, still recognises its nesting.
		r.joiner, _ = tm.(coresql.Joiner)
		//: the bare retrying transactor.
		return r, nil
	}
	r.joiner = joiner
	//: every sibling forwards to the transactor the attempts run on.
	return &retryingSiblings{retrying: r, Joiner: joiner, Deferrer: deferrer, Aborter: aborter}, nil
}

// attempts applies the documented clamp.
func (c RetryConfig) attempts() int {
	//: an unset budget is not a request for no attempt at all.
	if c.Attempts <= 0 {
		//: the documented default.
		return DefaultRetryAttempts
	}
	//: the caller's own budget.
	return c.Attempts
}

// curve applies the documented clamp.
func (c RetryConfig) curve() kbackoff.Value {
	//: only the zero value is unset; any other curve is the caller's.
	if c.Backoff == (kbackoff.Value{}) {
		//: grown by 2, with the jitter that keeps two deadlocked transactions
		//: from meeting again.
		return kbackoff.Value{BaseDelay: DefaultRetryDelay, JitterMode: kbackoff.JitterFull}
	}
	//: the caller's curve, as given.
	return c.Backoff
}

// classifier applies the documented clamp.
func (c RetryConfig) classifier() coresql.Transient {
	//: the SDK's own reading of a driver's error.
	if c.Transient == nil {
		//: SQLSTATE and SQLite result codes.
		return Classify
	}
	//: the connector's own.
	return c.Transient
}

// clock applies the documented time-source fallback.
func (c RetryConfig) clock() clock.Timed {
	//: the wall clock is the only non-arbitrary default.
	if c.Clock == nil {
		//: never mutate clock.System at package scope.
		return clock.System
	}
	//: the caller's injected clock.
	return c.Clock
}

// Transact runs fn in a transaction of the inner transactor, and runs the
// whole transaction again while it fails on a transient error and the budget
// allows.
func (r *retrying) Transact(ctx context.Context, opts coresql.TxOptionsValue, fn coresql.TxFunc) error {
	//: a nested call joins its parent and is never run again on its own; a
	//: nil fn is the inner transactor's to refuse.
	if fn == nil || r.nested(ctx) {
		//: one run, exactly as the inner transactor decides it.
		return r.inner.Transact(ctx, opts, fn)
	}
	//: the root's context is marked, so a Transact on this transactor inside
	//: it is recognised as nested whatever the inner transactor offers.
	marked := context.WithValue(ctx, retryMark{owner: r}, struct{}{})
	for attempt := 1; ; attempt++ {
		err := r.inner.Transact(marked, opts, fn)
		//: a commit, a failure that is not the database asking for another
		//: run, or a caller that stopped waiting: this attempt is the answer.
		if err == nil || ctx.Err() != nil || !r.retriable(err) {
			//: the attempt's own outcome, as the inner transactor reported it.
			return err
		}
		//: the budget is spent: say so, beside the last attempt's error.
		if attempt >= r.attempts {
			//: both, so neither question loses its answer.
			return failed(coresql.TxRetriesExhausted, err, kerrs.Int("attempts", attempt))
		}
		//: the wait before the next attempt, cut short by the caller's end.
		if waitErr := r.wait(ctx, attempt); waitErr != nil {
			//: the last attempt's error, and why there is no next one.
			return errors.Join(err, waitErr)
		}
	}
}

// nested reports whether ctx already carries a transaction a Transact on r
// must join rather than open: one this transactor opened, or one the inner
// transactor opened itself.
func (r *retrying) nested(ctx context.Context) bool {
	//: a root of this transactor is running.
	if ctx.Value(retryMark{owner: r}) != nil {
		//: nested.
		return true
	}
	//: no Joiner: only this transactor's own mark can say.
	if r.joiner == nil {
		//: a root.
		return false
	}
	_, inTx := r.joiner.Join(ctx)
	//: the inner transactor's own answer.
	return inTx
}

// retriable reports whether err earns another attempt: never for a context's
// end, else the classifier's verdict.
func (r *retrying) retriable(err error) bool {
	//: the caller stopped waiting, whatever the database said.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		//: not transient.
		return false
	}
	//: the classifier's verdict.
	return r.transient(err)
}

// wait blocks for the curve's wait after the attempt-th failure, on the
// injected clock, or until ctx ends.
func (r *retrying) wait(ctx context.Context, attempt int) error {
	timer := r.clk.NewTimer(r.curve.Delay(attempt))
	defer timer.Stop()
	select {
	case <-timer.C():
		//: the next attempt may run.
		return nil
	case <-ctx.Done():
		//: the caller's end, in its own words.
		return context.Cause(ctx)
	}
}

// Classify is the SDK's [coresql.Transient]: true for a failure the database
// documents as "run the transaction again", read from the methods drivers
// expose rather than from a driver this package would have to import.
//
// A SQLSTATE of 40001 (serialization failure) or 40P01 (deadlock detected),
// read through a SQLState() string method — pgx's and lib/pq's errors offer
// one —, and a SQLite result code whose primary code is 5 (BUSY) or 6
// (LOCKED), extended forms such as 517 (BUSY_SNAPSHOT) included, read
// through a Code() int method — modernc.org/sqlite's errors offer one. Every
// error the chain joins or wraps is read, so a verdict joined beside the
// driver's error, or a [Withheld], still answers.
//
// A context's end is never transient, whatever else the chain carries.
// MySQL's deadlock (1213) and lock-wait timeout (1205) are not read here:
// go-sql-driver exposes them as fields, not methods, so a MySQL connector
// composes its own reading with this one.
func Classify(err error) bool {
	//: a context's end is never transient, whatever else the chain carries.
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		//: not transient.
		return false
	}
	//: every error the chain joins or wraps, the verdict's siblings included.
	return anyCause(err, transientCause)
}

// transientCause reads one error of a chain: a SQLSTATE or a SQLite result
// code that asks for the transaction to run again.
func transientCause(err error) bool {
	//: pgx's PgError and lib/pq's Error name their SQLSTATE through a method.
	if stated, ok := err.(interface{ SQLState() string }); ok {
		switch stated.SQLState() {
		//: serialization_failure, deadlock_detected.
		case "40001", "40P01":
			//: the database asks for another run.
			return true
		}
		//: another class of failure.
		return false
	}
	//: modernc.org/sqlite's Error names SQLite's result code through one.
	if coded, ok := err.(interface{ Code() int }); ok {
		switch coded.Code() & sqlitePrimary {
		//: BUSY and LOCKED, any extended form.
		case sqliteBusy, sqliteLocked:
			//: another connection held what this one needed.
			return true
		}
	}
	//: nothing this reading recognises.
	return false
}

// anyCause reports whether match holds for err or for any error it wraps or
// joins, depth first.
func anyCause(err error, match func(error) bool) bool {
	//: the end of a branch.
	if err == nil {
		//: nothing here.
		return false
	}
	//: this node.
	if match(err) {
		//: found.
		return true
	}
	switch unwrapped := err.(type) {
	//: errors.Join and a multi-cause wrap.
	case interface{ Unwrap() []error }:
		for _, cause := range unwrapped.Unwrap() {
			//: any branch answers.
			if anyCause(cause, match) {
				//: found.
				return true
			}
		}
	//: a single-cause wrap.
	case interface{ Unwrap() error }:
		//: down the chain.
		return anyCause(unwrapped.Unwrap(), match)
	}
	//: no branch answered.
	return false
}
