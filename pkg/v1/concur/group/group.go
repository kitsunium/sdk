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
	kgroup "github.com/kitsunium/sdk/internal/kernel/concur/group"
)

// Unlimited is the concurrency limit that bounds nothing: a slot is always
// free, so [Group].Go never waits for one. It has to be written out — a zero
// limit is clamped to one, never read as "no limit".
const Unlimited int = kgroup.Unlimited
