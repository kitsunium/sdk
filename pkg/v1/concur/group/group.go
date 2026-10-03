//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/concur/group .

// Package group runs a set of tasks concurrently and gives their caller one
// place to wait: one bounded degree of parallelism, one error — the first, or
// every one of them — and the panic a child goroutine would otherwise take the
// whole process down with, delivered to the waiter instead.
//
//	g, ctx := group.New(ctx, 8) // at most eight tasks at once
//	for _, url := range urls {
//	    g.Go(func(ctx context.Context) error { return fetch(ctx, url) })
//	}
//	err := g.Wait() // every task has returned; the first failure, or nil
//
// It is the SDK's own structured-concurrency primitive, published as an alias
// of the kernel package its services run on (ADR 0159 §4): the type a program
// holds is the type the SDK holds.
//
// # When to use it
//
// Reach for it wherever you would write a sync.WaitGroup together with an
// error variable, a mutex and a semaphore channel: N tasks, one wait, at most L
// at a time. It does the job of golang.org/x/sync/errgroup with no module
// outside the standard library, and says what it does where a default would
// otherwise decide: a zero limit, a task's panic, and more than one failure.
//
// # One error, or every error
//
// A group from [New] reports the FIRST error, because almost every error after
// it is a consequence of the cancellation the first one caused. A group from
// [NewJoined] reports EVERY task's error, joined with errors.Join in
// submission order — for tasks whose failures are independent facts, such as
// workers polling one broken medium. Both cancel the siblings on the first
// failure, and in both the first failure is the context's context.Cause.
//
// [Collect] is the typed fan-out: each task returns a value, and the results
// come back in SUBMISSION order whatever order the tasks finished in.
//
// # A panic in a task is delivered, not fatal
//
// A goroutine that panics and is not recovered on its own stack kills the
// process, and no recover in the parent can see it. Here the recovery happens
// inside the task's goroutine, with that goroutine's stack, and
// [Group].Wait re-raises it as a [PanicValue] — after every other task has
// returned, so when Wait leaves, by return or by panic, the group owns no
// running goroutine. A PanicValue is deliberately not an error: a panic says
// the result is not one.
//
// # What Wait guarantees, and what it cannot
//
// Wait returns when every task has RETURNED, not when the context is
// cancelled: cancellation is a request, and a goroutine cannot be forced to
// honour it. A task that ignores its context keeps Wait blocked for as long as
// it runs — bound it from the inside, with a deadline on the I/O it performs.
// A task submitted after the group failed still runs, with a cancelled
// context, rather than being skipped without a word.
//
// # Limits
//
// The limit is taken on the submitting goroutine before a goroutine exists, so
// N submissions against a limit of L hold L goroutines, not N. A non-positive
// limit is clamped to 1 — one task at a time, slow but correct — and no limit
// at all is spelled [Unlimited]: a zero read as "unbounded" would be a value
// nobody chose, and a zero read as "nothing may run" is the deadlock
// errgroup's SetLimit(0) produces.
//
// A [Group] is used once: after Wait has returned, submit to a new one. Go may
// be called from any goroutine, but not concurrently with Wait.
package group

import (
	"context"

	kgroup "github.com/kitsunium/sdk/internal/kernel/concur/group"
)

// Unlimited is the concurrency limit that bounds nothing: a slot is always
// free, so [Group].Go never waits for one. It has to be written out — a zero
// limit is clamped to one, never read as "no limit".
const Unlimited int = kgroup.Unlimited

// Group runs tasks concurrently under one context and one wait point.
//
// [Group].Go starts a task on a goroutine of its own, blocking while the group
// already runs its limit of tasks; [Group].Wait blocks until every task has
// returned, cancels the group's context, and returns the first error — or,
// for a group from [NewJoined], every error joined — re-raising a task's
// panic as a [PanicValue] if one panicked.
//
// The zero value is NOT usable: build it with [New] or [NewJoined], which also
// return the context the tasks receive.
type Group = kgroup.Group

// PanicValue carries a panic raised inside a task across the goroutine
// boundary to whoever waits on the group: Raised is the value the panic
// carried, verbatim, and Stack the stack of the goroutine that ran the task,
// captured at recovery. Its String method renders both, so an uncaught
// re-raise prints the task's stack rather than only the waiter's.
//
// It is deliberately NOT an error, and carries no error code: a panic is a
// programming fault, and a value the caller may ignore is how a broken
// invariant becomes a silent wrong answer.
type PanicValue = kgroup.PanicValue

// New returns a [Group] reporting the first error, and the context every task
// submitted to it receives.
//
// limit bounds how many tasks run at once; a non-positive limit is clamped to
// 1, and [Unlimited] removes the bound. The returned context is cancelled when
// the first task fails — that task's error is its context.Cause —, when a
// task panics, or when [Group].Wait returns, whichever comes first.
func New(parent context.Context, limit int) (*Group, context.Context) {
	//: the kernel owns the group; this facade only forwards.
	return kgroup.New(parent, limit)
}

// NewJoined returns a [Group] whose [Group].Wait reports EVERY task's error,
// joined with errors.Join in the order the tasks were submitted, rather than
// the first one. Everything else is [New]'s: the limit and its clamp, the
// context, the siblings cancelled on the first failure — still the context's
// context.Cause — and a panic, which outranks every error.
//
// The joined error is matchable as each of its parts — errors.Is, errors.As
// and the SDK's errs.HasCode walk Unwrap() []error — and Wait returns nil,
// not an empty join, when no task failed.
func NewJoined(parent context.Context, limit int) (*Group, context.Context) {
	//: the kernel owns the group; this facade only forwards.
	return kgroup.NewJoined(parent, limit)
}

// Collect runs every function in fns concurrently, at most limit at a time
// ([New]'s clamp, [Unlimited] for none), and returns their results in
// SUBMISSION order: index i of the result is fns[i]'s value.
//
// On the first error the remaining functions see a cancelled context and
// Collect returns (nil, err) — never a half-filled slice, whose zero values
// would read as answers. A panic in any function propagates out of Collect as
// a [PanicValue], after every other function has returned.
//
// It is a function rather than a method because Go's methods take no type
// parameters of their own: a [Group] carrying a result type would force one on
// every caller who only wants to wait.
func Collect[T any](parent context.Context, limit int, fns []func(ctx context.Context) (value T, err error)) (results []T, err error) {
	//: the kernel owns the fan-out; this facade only forwards.
	return kgroup.Collect(parent, limit, fns)
}
