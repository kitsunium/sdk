<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/

## Purpose

Chainable `core/logger.Sink` decorators. Each sub-package is itself a
`Sink` so middlewares compose by wrapping one another — the bottom of the
chain is always a terminal sink from `service/logger/sink/*`.

## Contents

| Package    | Behaviour | Code prefix |
|---|---|---|
| `multi/`   | Fan-out broadcast to N branches; errors aggregated via `errors.Join` | 0.3.16.\* |
| `async/`   | Non-blocking ring + drainer goroutine in front of a slow downstream  | 0.3.17.\* |
| `route/`   | Predicate-based dispatch (`Params{When, Sink}`) + optional fallback  | 0.3.18.\* |
| `failover/`| Sequential retry across an ordered chain                              | 0.3.19.\* |
| `sample/`  | 1-of-N rate-limiter on top of a downstream sink                       | 0.3.20.\* |
| `recover/` | Catches downstream panics, materialises them as typed errors          | 0.3.21.\* |

## Composition order

Recommended outer-to-inner order when chaining (producer at the top):

```
producer
  └── async       (decouple hot path from slow I/O)
       └── recover (catch panics close to the producer)
            └── failover (try fallbacks before sampling)
                 └── sample  (rate-limit before fanning out)
                      └── multi (fan-out)
                           ├── route (dispatch to specialised drains)
                           │     ├── sink/console
                           │     └── sink/syslog
                           └── sink/file
```

The chain is a recommendation, not a hard rule — the order is dictated by
intent: `async` near the producer to never block; `recover` outside any
sink that might panic; `multi` / `route` close to the terminal sinks.

## Cost

Every middleware here is benchmarked against **one shared discard control in
one run**, so the delta from that control is the middleware. The report is
`internal/service/logger/BENCH.md` (§1 per middleware, §0 for the allocation
verdict); there is deliberately no per-package BENCH.md, because a number taken
in a different run against a different control is not comparable.

Per record, on an 8-core EPYC 7351P — see the report for the envelope:

| middleware | cost over a bare sink | allocs |
|---|---:|---:|
| `sample` (record dropped) | +5.9 ns | 0 |
| `failover` (first branch accepts) | +9.4 ns | 0 |
| `multi` | +8.8 ns fixed, +8.4 ns per destination | 0 |
| `route` | +12.6 ns, +5.5 ns per rejected predicate | 0 |
| `sample` (record kept) | +12.7 ns | 0 |
| `tee` | +16.1 ns fixed, +7.9 ns per primary | 0 |
| `recover` | +20.5 ns, panic or not | 0 |
| `async` (publisher hand-off) | +188 ns | 0 |
| `encwrite` | +1 311 ns | 6 |

Three findings a caller should know before wiring a chain:

- **Depth is nearly free; stacking four middlewares costs ~60 ns on a ~900 ns
  emit and no allocations.** Fan-out WIDTH was the thing that used to cost —
  see `multi/CLAUDE.md` and `failover/CLAUDE.md`.
- **`async` is not a universal win.** The hand-off is ~196 ns, a third of it
  the `ringMu` handshake with the drainer, so it pays for itself only in front
  of a sink slower than that — a network drain, not a console. And an unpaced
  producer starves its own drainer: a tight loop dropped **93 % of records**
  even with a 65 536-slot ring, silently unless `OnDrop` is wired. Wire it.
- **`recover` costs 2.8× a whole discard sink on every call**, panic or not.
  It is cheap next to real I/O and expensive next to nothing; place it where a
  sink might genuinely panic rather than by reflex.

## Conventions

- **One package per concern**, each with its own `codes.go` + `errors.go`
  + sentinels named after their drop / fail mode.
- **OnError / OnDrop hooks.** Middlewares that can silently lose records
  expose a `Config` callback so operators wire metrics or fallback logs.
  `async` exposes both; others surface via aggregated errors.
- **Concurrency.** Every middleware is safe for concurrent producers by
  contract (`async` via ring + mutex, `multi` / `failover` / `route` via
  stateless fan-out, `sample` via `atomic.Uint64`).
- **Origin wins on wrap.** Aggregated errors flow through `errs.Wrap` so
  consumer-side `errors.Is` matches every downstream cause via `Unwrap`.

## Do NOT

- Add I/O to a middleware — they wrap, never originate, except for `async`
  whose drainer goroutine *is* the I/O boundary by design.
- Leak the concrete decorator type at the public API; constructors return
  the `Sink` interface.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/...
```

## Subtree

See each sub-package's `CLAUDE.md` for behaviour, error codes, and edge
cases.
