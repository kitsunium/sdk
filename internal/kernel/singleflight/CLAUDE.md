# internal/kernel/singleflight/

## Purpose

Generic **call deduplication**: `Group[K comparable, V any]` makes N concurrent
callers naming the same key produce exactly ONE execution, and hands its result
to all N. A kernel primitive (stdlib-only AND generic — `Group` / `Do` /
`Forget`, no domain word appears in any signature). Admitted by **ADR 0049**.
Emits **no error codes**: it is transparent to whatever `fn` returns, and its
only failure of its own is a programming fault, which panics.

## Contents

| File | Surface |
|---|---|
| `singleflight.go` | `Group[K,V]` + `Do` / `Forget` / `InFlight`, and the join/run/publish/await/abandon path |
| `call.go` | `call[V]` — the in-flight record shared by every caller of one key |
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

So the shared call gets a context of its own:
`context.WithCancel(context.WithoutCancel(leaderCtx))`. That is one decision
with three named consequences:

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
- **A served caller does not touch the group mutex.** `publish` retires the
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
- Cross-OS: 100 % portable (`sync`, `context`, `runtime/debug`).

## Do NOT

- Use it as a cache. Nothing is remembered after the call completes; two
  SEQUENTIAL `Do` calls run `fn` twice. Caching is `kernel/cache`, and the
  combination of the two is `internal/service/cache` (ADR 0049).
- Use it around work that costs less than a few microseconds — see `BENCH.md`.
- Run `fn` inline on the leader's goroutine "for speed". That is the exact
  defect this package exists to avoid, and
  `TestAbandonedLeaderDoesNotCondemnTheFollowers` fails on it.
- Add error codes. `fn`'s error is passed through untouched; the only failure
  the package owns is a panic.

## Verification

```
cd internal/kernel && GOWORK=off go test -race -count=10 ./singleflight/
bazel test --config=race //internal/kernel/singleflight:singleflight_test
```
