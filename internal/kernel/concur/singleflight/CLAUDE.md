<!-- updated: 2026-11-07T20:30:00Z -->
# internal/kernel/concur/singleflight/

## Purpose

Generic **call deduplication**: `Group[K comparable, V any]` makes N concurrent
callers naming the same key produce exactly ONE execution, and hands its result
to all N. A kernel primitive (stdlib-only AND generic — `Group` / `Do` /
`Forget`, no domain word appears in any signature). Admitted by **ADR 0049**.
Emits **no error codes**: it is transparent to whatever `fn` returns, and its
only failure of its own is a programming fault, which panics.

**Published as `pkg/v1/concur/singleflight`** (ADR 0159 §4): a pure alias, so every exported shape here is public API — a renamed field, a changed signature or a new method on an exported interface breaks a consumer at compile time, allowed only while the module is v0 and said out loud (ADR 0040).

## Contents

| File | Surface |
|---|---|
| `singleflight.go` | `Group[K,V]` + `Do` / `Forget` / `InFlight`, its sixteen `shard`s, and the join/run/publish/await/abandon path |
| `call.go` | `call[V]` — the in-flight record shared by every caller of one key — and `detached`, the root of its shared context (#813) |
| `panic.go` | `PanicValue` — the panic carried out of the shared call into every waiter |

`Group` has **no constructor**: its zero value is usable, as `sync.Mutex`,
`sync.WaitGroup` and `sync.Map` are, and the map it builds lazily is the only
state it owns. A `NewGroup` would be a second way in that adds nothing.

## Why a goroutine

`fn` runs on a goroutine of its own, not on the first caller's. The obvious
implementation (run it inline, on the leader) has a defect that only appears
under load: **the caller who arrived first is the one most likely to give up
first** — it has been waiting longest — and when its context is cancelled,
every other caller inherits that cancellation. One abandoned request fails ten
that were still willing to wait.

So the shared call gets a context of its own: `context.WithCancel` over the
leader's context with its cancellation removed — what
`context.WithoutCancel(leaderCtx)` returns, kept in the call record since #813
(see Conventions). That is one decision with three named consequences:

- **A caller that abandons is a departure, not a kill.** It stops waiting and
  receives its OWN `ctx.Err()`. The call continues.
- **The call is cancelled when the LAST caller leaves**, tracked by a refcount
  under the group mutex. Nothing is computed for an audience of zero.
- **The shared call inherits the FIRST caller's context values and no caller's
  deadline.** A follower expecting its own request-scoped values (trace id,
  tenant) sees the leader's. That is not a bug to be fixed later; one execution
  can carry only one set of values, and this is what deduplication means.

The price is measured, not assumed: **≈ 2 µs per leading call**, almost all of
it a goroutine park/unpark round trip. See `BENCH.md` — it names the threshold
below which this package should not be used at all.

## A panic is delivered, never swallowed

If `fn` panics, the recovered value **and the stack of the goroutine that
raised it** are captured and re-raised in every waiter as a `PanicValue`.

The two alternatives were both rejected. Recovering into an `error` converts a
programming fault into a runtime condition the caller may ignore, and loses the
originating stack. Letting the panic escape the goroutine takes the process
down *while the waiters are still blocked on a channel that will never close* —
the worst of the three, because the crash dump then points at N goroutines
parked in `await` and not at the code that failed.

One panic becoming N is the honest arithmetic: N callers asked for a result and
none of them can have one. The key is still retired, so the next `Do` on it
starts cleanly.

## One process, and only one

`Group` deduplicates **within one process**. Ten replicas of a service each
running a `Group` still send ten concurrent calls to whatever is behind them.
Cross-process coordination needs a shared lock or lease, which is a distributed
system and not a kernel primitive.

## Conventions

- **`Do` returns `(V, bool, error)`** — the middle value is `shared`: false for
  the caller that led the call, true for one that joined. Useful for a metric;
  never for a decision, since it describes the past.
- **Sixteen shards, each a map under its own mutex** (#1035). A key's shard is
  the low bits of `maphash.Comparable` over a seed drawn once per process; the
  array is fixed, so the zero `Group` still needs no constructor, and each
  shard is padded to 64 bytes so two never share a cache line. Distinct keys
  contend only when they hash to the same shard — under a miss storm a cache's
  `Load` leads one call per missed key, and one group-wide mutex used to
  serialise every leader. `InFlight` sums the shards one lock at a time;
  `Forget`, `publish` and `abandon` lock one.
- **The critical section holds the map and nothing else** (#1035). A follower
  finds the call and counts itself in one acquisition (`joinExisting`). A
  leader looks, builds its call record and derives its context with the lock
  released, then takes it again to insert (`insertOrJoin`); a candidate that
  lost the key to another leader in between cancels the context it built and
  joins the winner, and its `fn` never runs
  (`TestALosingLeaderCandidateIsCancelledAndNeverRuns`). The `go` statement
  runs with no lock held.
- **A served caller does not touch the shard's mutex.** `publish` retires the
  entry under the lock before closing `done`, so the deduplicated path costs
  exactly one mutex acquisition (the join).
- **Cancellation preempts waiting, not delivery.** `await` re-checks `done`
  after `ctx.Done()` wins the select, because Go picks at random among ready
  cases and would otherwise discard an existing answer on a coin flip.
  `TestAwaitPrefersAnAvailableResultOverACancelledContext` runs the branch 1000
  times for exactly that reason.
- **`Forget(key)`** drops the dedup entry so the NEXT caller starts fresh; it
  does not abandon the call in flight, and existing waiters still get its
  result.
- **`InFlight()` counts dedup entries, not running calls** — the keys a `Do`
  arriving now would join. A forgotten or fully abandoned call keeps running
  uncounted, so after `Forget` plus a re-issuing `Do` it reads 1 while two
  calls for that key run. Read it as a gauge, never as "how much work is live".
- **The shared context's root lives in the call record** (#813). `detached`
  (`call.go`) answers as `context.WithoutCancel(leaderCtx)` does — the
  leader's values, no deadline, a nil `Done`, a nil `Err`, the same name —
  and `join` builds `context.WithCancel(&candidate.base)`: a leading call
  allocates 5 objects where it allocated 6, B/op unchanged (`BENCH.md`; the
  budget on `(*Group[...]).Do` in `design/sdk.yaml`, fixture
  `perfDoLeading`). It is faithful only because fn never sees the root: the
  `WithCancel` above it answers `Cause`, a child's lookup of its parent and
  the private key `WithoutCancel` answers nil to, and finds `Done` nil so it
  never watches it. `identity_internal_test.go` holds the equivalence
  against the construction before #813, which it was written against and
  passes on. The price is reachability: fn's context keeps the whole record
  alive, so an fn that hands its context to work outliving the call keeps
  the call's result alive with it.
- Cross-OS: 100 % portable (`sync`, `context`, `hash/maphash`, `reflect`, `runtime/debug`, `strings`, `time`).

## Do NOT

- Use it as a cache. Nothing is remembered after the call completes; two
  SEQUENTIAL `Do` calls run `fn` twice. Caching is `kernel/collections/cache`, and the
  combination of the two is `internal/service/data/cache` (ADR 0049).
- Use it around work that costs less than a few microseconds — see `BENCH.md`.
- Run `fn` inline on the leader's goroutine "for speed". That is the exact
  defect this package exists to avoid, and
  `TestAbandonedLeaderDoesNotCondemnTheFollowers` fails on it.
- Add error codes. `fn`'s error is passed through untouched; the only failure
  the package owns is a panic.
- Hand `&call.base` to anything but the `context.WithCancel` in `join`. Asked
  directly, the root answers the context package's private key with the
  leader's own cancel context when it has one, where `WithoutCancel` answers
  nil — so `context.Cause` over it would read the leader's cause.
- Replace the root and the `WithCancel` by one context type that cancels
  itself (the shape #813 sketched). It cannot answer that private key as
  `WithoutCancel` does, so `context.Cause` would leak the leader's cause; and
  the context package wraps or watches, with a goroutine each, the children
  of a cancelling context it does not know — their names and their cost
  change. The saving is the same one allocation either way.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=10 ./concur/singleflight/
bazel test --config=race //internal/kernel/concur/singleflight:singleflight_test
go test -run Perf ./internal/kernel/concur/singleflight/   # race off: the allocs budget
```
