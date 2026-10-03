package group_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/kernel/group"
)

// errFirst and errSecond are two distinct task failures; errs-coded so the
// joined error is checked against the SDK's own matcher as well as errors.Is.
var (
	errFirst = errs.Define(errs.Pack(2, 3, 250, 1), "GROUP_TEST_FIRST",
		"group test first failure", "internal/kernel/group test: first")
	errSecond = errs.Define(errs.Pack(2, 3, 250, 2), "GROUP_TEST_SECOND",
		"group test second failure", "internal/kernel/group test: second")
)

// TestJoinedWaitReportsEveryFailureInSubmissionOrder pins what NewJoined
// exists for: every task's failure reaches the caller — not the first one —
// in the order the tasks were SUBMITTED, whatever order they failed in. The
// later task is made to fail first, so a list kept in completion order would
// come back reversed.
func TestJoinedWaitReportsEveryFailureInSubmissionOrder(t *testing.T) {
	t.Parallel()
	g, _ := group.NewJoined(t.Context(), group.Unlimited)
	secondFailed := make(chan struct{})
	g.Go(func(context.Context) error {
		//: the first submission fails LAST.
		<-secondFailed
		return errFirst
	})
	g.Go(func(context.Context) error {
		defer close(secondFailed)
		return errSecond
	})
	g.Go(func(context.Context) error { return nil })
	err := g.Wait()
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		t.Fatalf("Wait() = %v (%T), want an errors.Join of every failure", err, err)
	}
	parts := joined.Unwrap()
	if len(parts) != 2 || !errors.Is(parts[0], errFirst) || !errors.Is(parts[1], errSecond) {
		t.Fatalf("Wait() joined %v, want [first second] in submission order", parts)
	}
	//: matchable as each part, by the stdlib and by the SDK's own matcher.
	if !errors.Is(err, errFirst) || !errors.Is(err, errSecond) {
		t.Errorf("errors.Is does not reach both parts of %v", err)
	}
	if !errs.HasCode(err, errs.Pack(2, 3, 250, 1)) || !errs.HasCode(err, errs.Pack(2, 3, 250, 2)) {
		t.Errorf("errs.HasCode does not reach both parts of %v", err)
	}
}

// TestJoinedGroupStillCancelsOnTheFirstFailure pins that joining is about what
// Wait REPORTS, not about what the group does on a failure: the siblings are
// cancelled on the first one, and it is the context's cause.
func TestJoinedGroupStillCancelsOnTheFirstFailure(t *testing.T) {
	t.Parallel()
	g, ctx := group.NewJoined(t.Context(), group.Unlimited)
	observed := make(chan error, 1)
	g.Go(func(context.Context) error { return errFirst })
	g.Go(func(taskCtx context.Context) error {
		<-taskCtx.Done()
		observed <- context.Cause(taskCtx)
		//: a cancelled sibling that reports nothing adds nothing to the join.
		return nil
	})
	err := g.Wait()
	if !errors.Is(err, errFirst) {
		t.Fatalf("Wait() = %v, want the failure", err)
	}
	if cause := <-observed; !errors.Is(cause, errFirst) {
		t.Errorf("a sibling read context.Cause = %v, want the first failure", cause)
	}
	if cause := context.Cause(ctx); !errors.Is(cause, errFirst) {
		t.Errorf("context.Cause(returned ctx) = %v, want the first failure", cause)
	}
}

// TestJoinedWaitIsNilWhenNothingFailed pins that a joined group with no
// failure returns a genuine nil — never an empty join a caller would read as
// a failure.
func TestJoinedWaitIsNilWhenNothingFailed(t *testing.T) {
	t.Parallel()
	type tc struct {
		name  string
		tasks int
	}
	tests := []tc{
		{"no task submitted", 0},
		{"every task succeeded", 4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		g, _ := group.NewJoined(t.Context(), 2)
		for range c.tasks {
			g.Go(func(context.Context) error { return nil })
		}
		if err := g.Wait(); err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// TestJoinedGroupReRaisesAPanic pins that a panic outranks every error in a
// joined group exactly as in a first-error one: it is re-raised in Wait, after
// every sibling has returned.
func TestJoinedGroupReRaisesAPanic(t *testing.T) {
	t.Parallel()
	g, _ := group.NewJoined(t.Context(), group.Unlimited)
	g.Go(func(context.Context) error { return errFirst })
	g.Go(func(context.Context) error { panic("joined group task") })
	defer func() {
		raised := recover()
		value, ok := raised.(group.PanicValue)
		if !ok {
			t.Fatalf("Wait re-raised %v (%T), want a group.PanicValue", raised, raised)
		}
		if value.Raised != "joined group task" {
			t.Errorf("PanicValue.Raised = %v, want the task's own value", value.Raised)
		}
	}()
	err := g.Wait()
	t.Fatalf("Wait() returned %v instead of re-raising the panic", err)
}
