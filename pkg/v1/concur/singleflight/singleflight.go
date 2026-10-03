//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/concur/singleflight .

// Package singleflight deduplicates concurrent work that names the same key:
// of N goroutines asking for the same thing at the same time, exactly one does
// the work and all N receive its result.
//
//	var profiles singleflight.Group[string, Profile] // the zero value is ready
//
//	p, shared, err := profiles.Do(ctx, id, func(ctx context.Context) (Profile, error) {
//	    return db.Profile(ctx, id) // one query, however many callers arrived
//	})
//
// It is the primitive the SDK's cache domain collapses a stampede of misses
// with, published as an alias of that kernel package (ADR 0159 §4).
//
// # When to use it
//
// When a burst of identical requests would each hit the same origin — a hot
// key expiring in front of a database, a token refresh, a configuration
// fetch — and one answer serves them all. It does the job of
// golang.org/x/sync/singleflight with no module outside the standard library,
// typed by the key and the value, and with a context — which raises the
// question the next section answers: whose.
//
// # The shared call outlives any single caller
//
// The call runs on a goroutine of its own, under the FIRST caller's context
// with its cancellation removed, and that context is cancelled only when the
// LAST caller has left. A caller that gives up gets its own ctx.Err() and
// stops waiting; the call keeps running for everyone else. When nobody is
// left, the work stops — nothing is computed for an audience of zero.
//
// Two consequences follow. The call sees the first caller's context VALUES and
// no caller's deadline — one execution carries one set of values. And a Do that
// has returned has not necessarily stopped fn: abandonment is a departure, not
// a kill.
//
// # A panic is delivered to every waiter
//
// If fn panics, the value and fn's stack are re-raised in EVERY waiting caller
// as a [PanicValue] — never swallowed into an error, and never left to take the
// process down while the waiters block forever.
//
// # What this is not
//
// It is not a cache: nothing is remembered once the call completes, so two
// SEQUENTIAL calls run fn twice — the SDK's cache domain is the remembering
// layer above it. It is not a lock: two DIFFERENT keys never wait for each
// other. And it is per PROCESS: N replicas each running it still send N calls
// to whatever is behind them.
package singleflight

import (
	ksingleflight "github.com/kitsunium/sdk/internal/kernel/concur/singleflight"
)

// Group deduplicates concurrent calls that name the same key. The zero value is
// ready to use; a Group must not be copied after first use, and is safe for
// any number of goroutines.
//
// [Group].Do runs fn for a key unless the same key is already in flight, in
// which case it waits for that call instead; it returns the value, whether the
// result came from a call this goroutine did not start (shared), and the
// error. fn receives the shared call's context, not the caller's — see the
// package documentation. A caller whose ctx ends while it waits returns
// ctx.Err() and leaves the call running; a result already available is
// delivered even if ctx is also done.
//
// [Group].Forget retires a key so the NEXT Do starts a fresh call — for an
// in-flight call known to answer a question that has since changed; callers
// already waiting keep waiting and still receive its result.
//
// [Group].InFlight reports how many keys a Do arriving now would join rather
// than start — a point-in-time reading for a gauge or a test, never for a
// decision, and not a count of running calls (a forgotten or abandoned call
// may still run).
type Group[K comparable, V any] = ksingleflight.Group[K, V]

// PanicValue carries a panic raised inside a deduplicated call to every caller
// waiting on it: Raised is the value the panic carried, verbatim, and Stack the
// stack of the goroutine that ran fn, captured at recovery. Its String method
// renders both, so an uncaught re-raise prints fn's stack rather than only the
// waiter's.
//
// It is deliberately NOT an error, and carries no error code: a panic is a
// programming fault, not a runtime condition.
type PanicValue = ksingleflight.PanicValue
