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
