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
