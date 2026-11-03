// Package sql_test — the retrying transactor (#1058): which failures earn
// another attempt, how a nested call is kept out of the retry, and what the
// held functions of each attempt do.
package sql_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	coresql "github.com/kitsunium/sdk/internal/core/data/sql"
	kbackoff "github.com/kitsunium/sdk/internal/kernel/backoff"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcsql "github.com/kitsunium/sdk/internal/service/data/sql"
)

// retryStep is the deterministic curve the suite waits on: 10 ms, then 20 ms.
var retryStep = kbackoff.Value{BaseDelay: 10 * time.Millisecond}

// newRetrying wraps a transactor over f in a retrying one on clk.
func newRetrying(t *testing.T, f *fakeDB, clk clock.Timed, attempts int) coresql.Transactor {
	t.Helper()
	tm, err := svcsql.NewRetrying(newTransactor(t, f), svcsql.RetryConfig{
		Attempts: attempts, Backoff: retryStep, Clock: clk,
	})
	if err != nil {
		t.Fatalf("NewRetrying: %v", err)
	}
	return tm
}

// manualClock is a fresh ManualClock.
func manualClock() *clock.ManualClock {
	return clock.NewManualClock(time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC))
}

// TestARetriedCommitCommitsOnTheThirdAttemptAndRunsItsDeferredOnce is the
// issue's first acceptance: a COMMIT that fails with SQLSTATE 40001 twice
// then succeeds commits on the third attempt, the manual clock advanced by the
// backoff and no sleep, and what each attempt held with Defer runs once — the
// two attempts that rolled back dropped theirs.
func TestARetriedCommitCommitsOnTheThirdAttemptAndRunsItsDeferredOnce(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	serialization := &stateError{state: "40001"}
	f.commitFailures = []error{serialization, serialization}
	clk := manualClock()
	tm := newRetrying(t, f, clk, 3)
	var runs, deferred atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, ex coresql.Executor) error {
			runs.Add(1)
			if !tm.(coresql.Deferrer).Defer(ctx, func() { deferred.Add(1) }) {
				t.Error("Defer was not held inside the transaction")
			}
			_, err := ex.ExecContext(ctx, "UPDATE t SET c = c + 1")
			return err
		})
	}()
	start := clk.Now()
	for _, wait := range []time.Duration{10 * time.Millisecond, 20 * time.Millisecond} {
		clk.BlockUntil(1)
		clk.Advance(wait)
	}
	if err := <-done; err != nil {
		t.Fatalf("Transact = %v, want a commit on the third attempt", err)
	}
	if got := clk.Since(start); got != 30*time.Millisecond {
		t.Errorf("the clock advanced %v, want the curve's 10 ms + 20 ms", got)
	}
	if got := runs.Load(); got != 3 {
		t.Errorf("the unit of work ran %d times, want 3", got)
	}
	if got := deferred.Load(); got != 1 {
		t.Errorf("the deferred function ran %d times, want once — after the commit that stood", got)
	}
	want := []string{
		"BEGIN", "UPDATE t SET c = c + 1", "COMMIT",
		"BEGIN", "UPDATE t SET c = c + 1", "COMMIT",
		"BEGIN", "UPDATE t SET c = c + 1", "COMMIT",
	}
	if got := f.statements(); !slices.Equal(got, want) {
		t.Fatalf("statements = %v, want %v", got, want)
	}
}

// TestANestedTransactUnderARetryingRootIsNeverRunAgainAlone is the second
// acceptance: a nested call joins its parent as a savepoint and returns its
// transient failure after one run — a savepoint cannot be retried without the
// transaction around it — and a root whose failure is not transient returns it
// after one attempt.
func TestANestedTransactUnderARetryingRootIsNeverRunAgainAlone(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	tm := newRetrying(t, f, manualClock(), 3)
	deadlock := &stateError{state: "40P01"}
	var nestedRuns atomic.Int32
	err := tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		nestedErr := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			nestedRuns.Add(1)
			return deadlock
		})
		if !errors.Is(nestedErr, deadlock) {
			t.Errorf("nested Transact = %v, want the deadlock, returned once", nestedErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact = %v, want the root to commit after handling its savepoint's failure", err)
	}
	if got := nestedRuns.Load(); got != 1 {
		t.Fatalf("the nested unit of work ran %d times, want once", got)
	}
	var rootRuns atomic.Int32
	err = tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
		rootRuns.Add(1)
		return errWork
	})
	if !errors.Is(err, errWork) || errs.HasCode(err, coresql.CodeTxRetriesExhausted) {
		t.Fatalf("Transact = %v, want the unit of work's own failure", err)
	}
	if got := rootRuns.Load(); got != 1 {
		t.Fatalf("a failure that is not transient ran %d attempts, want 1", got)
	}
}

// TestANestedCallOnTheInnerTransactionIsNotRetried pins the half of nesting
// the mark cannot see: a context that carries a transaction the INNER
// transactor opened itself is recognised through its Joiner, so the retrying
// transactor joins it rather than re-running a savepoint alone.
func TestANestedCallOnTheInnerTransactionIsNotRetried(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	inner := newTransactor(t, f)
	tm, err := svcsql.NewRetrying(inner, svcsql.RetryConfig{Attempts: 5, Backoff: retryStep, Clock: manualClock()})
	if err != nil {
		t.Fatalf("NewRetrying: %v", err)
	}
	var runs atomic.Int32
	err = inner.Transact(t.Context(), coresql.TxOptionsValue{}, func(ctx context.Context, _ coresql.Executor) error {
		nestedErr := tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			runs.Add(1)
			return &stateError{state: "40001"}
		})
		if errs.HasCode(nestedErr, coresql.CodeTxRetriesExhausted) {
			t.Errorf("nested Transact = %v, want one run and its own failure", nestedErr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Transact: %v", err)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("a savepoint of the inner transaction ran %d times, want once", got)
	}
}

// TestSpentAttemptsReturnTxRetriesExhaustedBesideTheLastError is the third
// acceptance: the code by errs.HasCode, the driver's error by errors.Is, and
// how many attempts ran in a field.
func TestSpentAttemptsReturnTxRetriesExhaustedBesideTheLastError(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	serialization := &stateError{state: "40001"}
	f.commitErr = serialization
	clk := manualClock()
	tm := newRetrying(t, f, clk, 2)
	done := make(chan error, 1)
	go func() {
		done <- tm.Transact(t.Context(), coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error {
			return nil
		})
	}()
	clk.BlockUntil(1)
	clk.Advance(10 * time.Millisecond)
	err := <-done
	if !errs.HasCode(err, coresql.CodeTxRetriesExhausted) {
		t.Fatalf("Transact = %v, want TX_RETRIES_EXHAUSTED", err)
	}
	if !errors.Is(err, serialization) || !errs.HasCode(err, coresql.CodeCommitFailed) {
		t.Fatalf("Transact = %v, want the last attempt's COMMIT_FAILED and driver error beside it", err)
	}
	if !hasFieldValue(verdictOf(err, coresql.CodeTxRetriesExhausted), "attempts", "2") {
		t.Fatalf("Transact = %v, want the verdict's field attempts=2", err)
	}
	if got := len(f.statements()); got != 4 {
		t.Fatalf("statements = %v, want two attempts of BEGIN and COMMIT", f.statements())
	}
}

// TestAContextThatEndsDuringTheWaitStopsTheRetry pins that the wait between
// two attempts is the caller's too: an end cuts it, and no further attempt
// opens.
func TestAContextThatEndsDuringTheWaitStopsTheRetry(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	f.commitErr = &stateError{state: "40001"}
	clk := manualClock()
	tm := newRetrying(t, f, clk, 5)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- tm.Transact(ctx, coresql.TxOptionsValue{}, func(context.Context, coresql.Executor) error { return nil })
	}()
	clk.BlockUntil(1)
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) || !errs.HasCode(err, coresql.CodeCommitFailed) {
		t.Fatalf("Transact = %v, want the last attempt's failure beside the cancellation", err)
	}
	if got := len(f.statements()); got != 2 {
		t.Fatalf("statements = %v, want one attempt", f.statements())
	}
}

// TestClassifyReadsWhatDriversExpose is Classify's table: the SQLSTATEs and
// SQLite codes that ask for another run, through a join and a Withheld, and
// never a context's end.
func TestClassifyReadsWhatDriversExpose(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"serialization failure", &stateError{state: "40001"}, true},
		{"deadlock detected", &stateError{state: "40P01"}, true},
		{"unique violation", &stateError{state: "23505"}, false},
		{"sqlite busy", &codedError{code: 5}, true},
		{"sqlite locked", &codedError{code: 6}, true},
		{"sqlite busy snapshot", &codedError{code: 517}, true},
		{"sqlite busy recovery", &codedError{code: 261}, true},
		{"sqlite constraint", &codedError{code: 19}, false},
		{"joined beside a verdict", errors.Join(coresql.CommitFailed, &stateError{state: "40001"}), true},
		{"withheld", svcsql.NewWithheld(&stateError{state: "40P01"}), true},
		{"a context's end", context.Canceled, false},
		{"a deadline beside a serialization failure", errors.Join(context.DeadlineExceeded, &stateError{state: "40001"}), false},
		{"bad connection", driver.ErrBadConn, false},
		{"the SDK's own verdict alone", coresql.CommitFailed, false},
	}
	for _, tc := range cases {
		if got := svcsql.Classify(tc.err); got != tc.want {
			t.Errorf("%s: Classify = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// TestTheSiblingsAreOfferedExactlyWhenTheInnerTransactorOffersThem pins that
// a type assertion on the retrying transactor never lies: over the SDK's
// transactor it is a Joiner, a Deferrer and an Aborter forwarding to it; over
// one that offers none, it is a Transactor alone.
func TestTheSiblingsAreOfferedExactlyWhenTheInnerTransactorOffersThem(t *testing.T) {
	t.Parallel()
	f := newFakeDB()
	full, err := svcsql.NewRetrying(newTransactor(t, f), svcsql.RetryConfig{})
	if err != nil {
		t.Fatalf("NewRetrying: %v", err)
	}
	if _, ok := full.(coresql.Joiner); !ok {
		t.Error("over the SDK's transactor the retrying one is not a Joiner")
	}
	if _, ok := full.(coresql.Deferrer); !ok {
		t.Error("over the SDK's transactor the retrying one is not a Deferrer")
	}
	if _, ok := full.(coresql.Aborter); !ok {
		t.Error("over the SDK's transactor the retrying one is not an Aborter")
	}
	bare, err := svcsql.NewRetrying(bareTransactor{}, svcsql.RetryConfig{})
	if err != nil {
		t.Fatalf("NewRetrying: %v", err)
	}
	if _, ok := bare.(coresql.Joiner); ok {
		t.Error("over a bare transactor the retrying one claims a Joiner it cannot forward")
	}
	if _, err := svcsql.NewRetrying(nil, svcsql.RetryConfig{}); !errs.HasCode(err, coresql.CodeConfigInvalid) {
		t.Errorf("NewRetrying(nil) = %v, want CONFIG_INVALID", err)
	}
}

// bareTransactor is a Transactor that offers none of the siblings.
type bareTransactor struct{}

// Transact runs fn with no executor.
func (bareTransactor) Transact(ctx context.Context, _ coresql.TxOptionsValue, fn coresql.TxFunc) error {
	return fn(ctx, nil)
}
