package group

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// errFirst and errSecond are the test's own failure values; production code
// may not build errors outside kernel/errs (SDK rule 2).
var (
	errFirst  = errors.New("first")
	errSecond = errors.New("second")
)

// TestFailKeepsTheFirstErrorAndDropsTheCascade pins the choice to report the
// failure that STARTED the shutdown. Almost every later error is a consequence
// of the cancellation this one triggers, and reporting them would bury the
// cause.
func TestFailKeepsTheFirstErrorAndDropsTheCascade(t *testing.T) {
	t.Parallel()
	g, _ := New(t.Context(), 1)
	g.fail(errFirst)
	g.fail(errSecond)
	if !errors.Is(g.err, errFirst) {
		t.Fatalf("g.err = %v, want %v", g.err, errFirst)
	}
	if cause := context.Cause(g.ctx); !errors.Is(cause, errFirst) {
		t.Fatalf("context.Cause = %v, want the FIRST error %v", cause, errFirst)
	}
}

// TestCapturePanicKeepsTheFirstPanic mirrors the error rule for faults: the
// second panic is almost always raised by a task reacting to the cancellation
// the first one caused.
func TestCapturePanicKeepsTheFirstPanic(t *testing.T) {
	t.Parallel()
	g, _ := New(t.Context(), 1)
	func() {
		defer g.capturePanic()
		panic("first panic")
	}()
	func() {
		defer g.capturePanic()
		panic("second panic")
	}()
	if g.panicked == nil {
		t.Fatal("no panic captured")
	}
	if got := g.panicked.Raised; got != "first panic" {
		t.Fatalf("captured %v, want the first panic", got)
	}
}

// TestCapturePanicIsANoOpOnTheNormalPath guards the overwhelmingly common
// case: a task that returns must leave no panic behind for Wait to re-raise.
func TestCapturePanicIsANoOpOnTheNormalPath(t *testing.T) {
	t.Parallel()
	g, _ := New(t.Context(), 1)
	func() { defer g.capturePanic() }()
	if g.panicked != nil {
		t.Fatalf("panicked = %+v, want nil", g.panicked)
	}
}

// TestPanicValueStringNamesItsOwnPackage is why this type is not shared with
// kernel/singleflight: the message a crash prints must say which primitive was
// running the code that failed.
func TestPanicValueStringNamesItsOwnPackage(t *testing.T) {
	t.Parallel()
	value := PanicValue{Raised: "boom", Stack: []byte("goroutine 1 [running]:\n")}
	rendered := value.String()
	if !strings.HasPrefix(rendered, "kernel/group: panic in task: boom") {
		t.Fatalf("String() = %q, want it to open with the package and the payload", rendered)
	}
	if !strings.Contains(rendered, "originating goroutine stack:") {
		t.Fatalf("String() = %q, want the stack section", rendered)
	}
}

// TestRenderRaisedCoversEveryPayloadShape checks each branch of the panic
// payload switch, including the fallback that refuses to guess.
func TestRenderRaisedCoversEveryPayloadShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		raised any
		want   string
	}{
		{"string", "plain message", "plain message"},
		{"error", errFirst, "first"},
		{"stringer", PanicValue{Raised: "inner"}, "kernel/group: panic in task: inner\noriginating goroutine stack:\n"},
		{"opaque", struct{ N int }{7}, "non-textual panic value (see PanicValue.Raised)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := renderRaised(tc.raised); got != tc.want {
				t.Fatalf("renderRaised(%v) = %q, want %q", tc.raised, got, tc.want)
			}
		})
	}
}

// TestNewClampsTheLimitToTheSemaphoreCapacity checks the clamp where it is
// implemented, complementing the observable-behaviour test in the external
// suite. A limit of zero must never produce a semaphore that admits nothing.
func TestNewClampsTheLimitToTheSemaphoreCapacity(t *testing.T) {
	t.Parallel()
	for _, limit := range []int{-7, 0, 1} {
		g, _ := New(t.Context(), limit)
		if got := cap(g.sem); got != 1 {
			t.Fatalf("New(ctx, %d): cap(sem) = %d, want 1", limit, got)
		}
	}
	g, _ := New(t.Context(), 5)
	if got := cap(g.sem); got != 5 {
		t.Fatalf("New(ctx, 5): cap(sem) = %d, want 5", got)
	}
}

// TestUnlimitedSemaphoreIsAllocatedNotApproximated guards the reason Unlimited
// can be math.MaxInt at all: a chan struct{} buffer is free at any size, so the
// constant is a real capacity rather than a sentinel the code has to branch on.
func TestUnlimitedSemaphoreIsAllocatedNotApproximated(t *testing.T) {
	t.Parallel()
	g, _ := New(t.Context(), Unlimited)
	if got := cap(g.sem); got != Unlimited {
		t.Fatalf("cap(sem) = %d, want %d", got, Unlimited)
	}
}
