// Package singleflight — the in-flight call record shared by every caller of
// one key.
//
// Package singleflight — the panic carried out of the shared call into every
// waiter.
//
// Package singleflight deduplicates concurrent work that names the same key:
// of N goroutines asking for the same thing at the same time, exactly one does
// the work and all N receive its result. It is a kernel primitive — stdlib
// only, fully generic, and free of domain vocabulary (Group, Do, Forget; no
// Cache, no Request, no Key that means something).
//
// # The shared call outlives any single caller
//
// The obvious implementation runs fn on the first caller's goroutine with the
// first caller's context. It has a defect that only shows up under load: the
// caller who arrived first is also the one most likely to give up first — it
// has been waiting longest — and when it does, every other caller inherits its
// cancellation. One abandoned request fails ten that were still willing to
// wait.
//
// So fn runs on a goroutine of its own, under a context derived from the first
// caller's with cancellation removed (context.WithoutCancel), and that context
// is cancelled only when the LAST caller has left. A caller that abandons
// stops waiting and gets its own ctx.Err(); the call keeps running for
// everyone else. When nobody is left, the work stops — nothing is computed for
// an audience of zero.
//
// Two consequences are worth stating rather than discovering:
//
//   - The shared call inherits the FIRST caller's context VALUES, and no
//     caller's deadline. A follower that expected its own request-scoped
//     values (a trace id, a tenant) will not see them; it sees the leader's.
//     Deduplication means one execution, and one execution can carry only one
//     set of values.
//   - Because fn runs on its own goroutine, a Do that has returned has not
//     necessarily stopped fn. Abandonment is a departure, not a kill.
//
// # A panic is delivered, never swallowed
//
// If fn panics, the recovered value and its stack are captured and re-raised
// in EVERY waiter as a [PanicValue]. The alternative — recover and report an
// error — turns a programming fault into a runtime condition and loses the
// original stack; the other alternative — let it escape the goroutine — takes
// the process down while the waiters are still blocked on a channel that will
// never close. One panic becoming N is the honest arithmetic: N callers asked
// for a result, and none of them can have one.
//
// # What this is NOT
//
// It is not a cache: nothing is remembered once the call completes, so two
// SEQUENTIAL Do calls run fn twice. It is not a lock: two DIFFERENT keys never
// wait for each other. And it is per-PROCESS — N replicas of a service each
// running this primitive still send N concurrent calls to whatever is behind
// them.
package singleflight
