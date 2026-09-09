# ADR 0039 — a published port is extended by a sibling interface, never by widening

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0025](0025-sdk-cache-kernel.md) (`kernel/cache`, whose `Config` publishes `Clock`), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values), [ADR 0009](0009-pkg-public-module-resolvability.md) / [ADR 0017](0017-pkg-bare-module-path.md) (what "published" means here)
- **Reverses**: the `Do NOT` in `internal/kernel/clock/CLAUDE.md` that said "Add `Sleep`, `After`, `NewTimer` here — no"

## Context

`clock.Clock` had two methods, `Now` and `Since`. It **read** time and could not
**wait** for it. Injecting it therefore made nothing temporal testable: no
timeout, no backoff, no ticker cadence. Several claims in this repo's own
planning rested on "inject the clock and it becomes testable without sleeping",
and measurement showed that claim was empty.

The obvious fix is to add `After`, `NewTimer`, `NewTicker` and `Sleep` to
`Clock`. It is also wrong, for a reason worth recording because it will recur
for every other port this SDK publishes.

**`Clock` is a published port.** `pkg/v1/cache.Config` is a *type alias* to
`kernel/cache.Config`, so its `Clock` field carries `internal/kernel/clock.Clock`
verbatim into the released `pkg` module. Go interfaces are **structural**: any
downstream type with `Now` and `Since` already satisfies it, without importing
anything or declaring intent. Adding a method to that interface breaks every one
of them **at compile time, with no deprecation window** — plus seven test
doubles inside this repo.

The `internal/` firewall does not help. What is published is the *shape*, and an
alias exports the shape.

## Decision

1. **Extend by adding a sibling interface, never by widening the published one.**
   - `Clock` keeps exactly `Now` and `Since`. Byte-identical.
   - `Waiter` is new: `After`, `NewTimer`, `NewTicker`, `Sleep`.
   - `Timed` is their union, for code that needs both.
   - `System` widens from `Clock` to `Timed`. Widening the **value** is safe —
     every existing `clk = clock.System` assignment into a `Clock` field still
     compiles. Widening the **interface** is what breaks callers.

   Zero implementers changed, in-tree or out.

2. **The guard is executable, not documentary.**
   `TestTwoMethodDoubleStillSatisfiesClock` declares a bare `Now`/`Since` type
   and assigns it to a `clock.Clock`. A future contributor who "tidies up" by
   folding `Waiter` back into `Clock` fails that named test before they reach
   review. A comment saying *don't* would not have survived — the one that was
   there is precisely what this ADR reverses.

3. **Zero durations split along ADR 0031's own line, rather than uniformly.**
   - `After` / `NewTimer` / `Sleep` / `Timer.Reset` with `d <= 0` **fire at
     once**: "already elapsed" is the only sensible reading, it is total, and it
     is the stdlib's own contract — the *clamp* half.
   - `NewTicker` / `Ticker.Reset` with `d <= 0` **refuse**, by panic, raised
     before `time.NewTicker` is reached so the stdlib's own panic never leaks.
     `System` and `ManualClock` refuse in identical words, asserted verbatim by
     tests. Panic rather than error matches the kernel's existing posture
     (`worker.Start` panics on a nil loop) and avoids claiming a code range for
     a programming error.

4. **`ManualClock` ships alongside `testing/synctest`, and the docs say when
   each wins** — measured, not asserted:
   - Inside a synctest bubble, **`clock.System` is already fake** (it delegates
     to package `time`). The intuition "you need a manual clock to get fake time
     under synctest" is **false**, and the package documentation says so.
   - synctest wins for code you cannot inject into, and its `Wait`/`Sleep`
     advance-and-settle has **no `ManualClock` equivalent** — stated as the
     largest gap rather than glossed.
   - `ManualClock` wins because `synctest.Test` documents that `T.Run`,
     `T.Parallel` and `T.Deadline` must not be called inside a bubble, which
     rules out this repo's table-driven-parallel convention; because real I/O is
     not durably blocking, so anything touching `rotfile`/`dbsink`/`proc` stalls
     the bubble; and because the bubble origin is fixed at 2000-01-01 UTC with
     no API to move it, while `NewManualClock` takes any instant and can rewind.

## Consequences

- **The rule generalises past `clock`.** Any port reachable through a `pkg/v1`
  alias is frozen in the same way. Before adding a method to a core or kernel
  interface, check whether an alias publishes it; if so, add a sibling.
- The seven in-tree two-method doubles keep compiling and are **not** migrated —
  that is its own change, not a rider on this one.
- `BENCH.md` was re-measured whole, with a control pair proving the adapter
  claim: `Stdlib_NewTimer` and `System_NewTimer` are both 248 B/op, 3 allocs/op.
  Wrapping `*time.Timer` behind the interface is allocation-free — measured, not
  assumed.
- No error codes, as with `cache` (ADR 0025). No `codeRangeOwners` entry.

## Why not

- **Widen `Clock` and fix the in-tree doubles.** Rejected: it fixes the seven we
  can see and breaks every downstream implementer we cannot, silently to us and
  at compile time for them.
- **Version the interface (`ClockV2`).** Rejected: it names the *change* rather
  than the *capability*, so the second extension produces `ClockV3` and nobody
  can tell from a signature what a parameter requires. `Waiter` says what it
  needs.
- **Skip `ManualClock` because `synctest` exists.** Rejected on the measured
  limits in Decision 4 — chiefly that the bubble forbids `T.Parallel`, which
  this repo uses in nearly every test.
- **Make `NewTicker(0)` return an error.** Rejected: it is a programming error,
  not a runtime condition, and the stdlib already panics. Refusing earlier with
  a better message is an improvement; converting it to a value the caller must
  check is a new contract for no gain.

## References

- `internal/kernel/clock/CLAUDE.md` (the reversed `Do NOT`, with the rationale inline)
- `internal/kernel/clock/waiter_external_test.go` (`TestTwoMethodDoubleStillSatisfiesClock`)
- `pkg/v1/cache/cache.go` — the type alias that makes `Clock` published
- `go doc testing/synctest` (go1.27.1)
