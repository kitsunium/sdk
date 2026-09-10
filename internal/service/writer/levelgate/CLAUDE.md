# internal/service/writer/levelgate/

## Purpose

A `core/logger.Sink` decorator that enforces a per-writer severity floor:
records with `Level >= min` reach the wrapped sink; the rest are **silently
dropped** as a successful `(len(p), nil)` no-op. Used by every writer factory
(console, file, s3, cloudwatch) to realise the optional `MinLevel` config field
(ADR 0012) without the `route` middleware's `NoMatch` error — a dropped record
must never surface as a fan-out failure.

## Contents

| File | Role |
|---|---|
| `gate.go` | `gateSink` + `New(inner, min)` + `Write` / `Flush` / `Close` |

No `codes.go` / `errors.go` — the gate never originates an error.

## Behaviour

- `New(inner, min)` returns `inner` **unwrapped** when `min == level.Info` (the
  zero value of a config's `MinLevel`, i.e. "inherit the handler-global level")
  — no wrapper, no per-record overhead.
- For any other `min`, `Write` forwards records at/above the floor and drops the
  rest, reporting `(len(p), nil)` so `middleware/multi` never aggregates a
  spurious failure for a deliberately-dropped record.
- `Flush` / `Close` delegate verbatim — the gate buffers nothing and owns no
  resources.

## Cost

Measured in `BENCH.md` (median of five runs, AMD EPYC 7351P). The numbers this
package is bought for are the first two rows, and they are the answer to "what
does a severity floor cost the records it discards".

| | ns/op | allocs |
|---|---:|---:|
| a **dropped** record (`min=Error`, record at `Info`) | **6.84** | **0** |
| the same record with no gate installed at all | 5.95 | 0 |
| a record the gate passes | 13.58 | 0 |
| `New(inner, Info)` — the "inherit" sentinel | 3.07 | 0 |
| `New(inner, Error)` — a real floor | 29.99 | 1 (24 B) |

- **A drop costs 0.89 ns over an ungated sink and removes a 302 ns write** — the
  cheapest destination this SDK has, and 1 868 ns if the destination is a
  container's stderr (`../console/BENCH.md` §1). The gate repays itself 340× on
  its first discarded record.
- **Neither direction allocates.** An allocation profile at `-memprofilerate=1`
  over 529 020 912 dropped records does not contain `gateSink.Write` at all.
  `TestGateAllocatesNothingInEitherDirection` pins it, mutation-checked in its
  own doc comment, and is covered by the race-off alloc lane
  (`tools/alloc-lane-targets.txt`, SDK-wide rule 12).
- **The comparison itself is not measurable** (0.05 ns against the same
  delegation with the branch deleted). What a passing record pays is a second
  interface hop carrying the 104-byte `RecordEvent` by value — 2.65 ns of which
  is the copy, 39 % of a whole drop. The port is frozen (ADR 0039) and the copy
  is also what stops a sink mutating a record its `multi` siblings will see;
  refused, with the price stated.
- **The immutable floor scales linearly**: 0.931 ns/op across eight goroutines
  against 6.89 ns serially — linear to within 9 %, i.e. no shared mutable state
  at all. A `Leveler`-based
  dynamic floor was measured and refused — see the Do NOT below.

## Do NOT

- Use this to express a per-writer floor of exactly `Info` while the handler
  global is lower — `min == Info` is the "inherit" sentinel and is unwrapped.
  Restrict via a higher level (`Warn` / `Error`) instead.
- Add buffering or I/O here — the gate is a pure pass-through filter.
- Make the floor dynamic by holding a `level.Leveler`. It was built and measured
  (`BENCH.md` §3): the atomic load is FREE — a `*level.Var` held concretely is
  indistinguishable from an immutable field, serial and at eight-way parallelism
  — but reading it through the `Leveler` **port** costs 1.19–1.23× per record
  for a capability nothing in the tree asks for, and an `RWMutex` spelling costs
  43.5× under contention and 160× with the floor actually moving. Reconfiguration is
  already `writer.Open` again. Note that `level.Var`'s own doc comment describes
  this arrangement ("a gate may read Level on the hot path while a control
  goroutine raises or lowers the floor via `Set`") and **no gate in this
  repository implements it** — that comment describes a consumer that does not
  exist.

## Verification

```sh
bazel test --config=race //internal/service/writer/levelgate:levelgate_test
# Fallback
cd internal/service && GOWORK=off go test -race -cover ./writer/levelgate/...
```
