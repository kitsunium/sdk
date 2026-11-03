<!-- updated: 2026-11-03T12:00:00Z -->
# internal/service/observe/logger/middleware/sample/

## Purpose

1-of-N rate-limiter `Sink` decorator. Every Nth `Write` is forwarded to
the downstream sink; the rest are silently dropped. A single
`atomic.Uint64` counter keeps concurrent producers correct without a
mutex on the hot path.

Use case: emit verbose `Debug` records at a 1/100 rate to a remote drain
while keeping the local console stream intact (compose with `multi` for
per-branch policies).

`NewKeyed` (#1017) is the sampler for a production stream: a counter per key
(the level and the message by default), the first `First` records of each
`Window` forwarded then every `Thereafter`-th, records at or above
`MinLevelBypass` (Warn) never sampled, every drop counted (`Dropped`) and
reported (`OnDrop`) — zap's `NewSamplerWithOptions`, on the injected clock.

## Contents

| File | Role |
|---|---|
| `sample_sink.go` | `sampleSink` + `New` + `Write` / `Flush` / `Close` |
| `keyed.go` | `Keyed` + `NewKeyed` + `KeyedConfig` + the `Default*` constants; `keep` / `admit` (the table bound and its sweep) / `advance` (the lazy window) / `forwards` |
| `perf_fixtures_test.go`, `gen_perf_test.go` | the `allocs: 0` budget of `(*Keyed).Write`, kept and dropped (alloc lane) |
| `internal/core/observe/logger/middleware/sample` | its sentinels — range 0.3.20.\* — declared in the core mirror since ADR 0160; this package declares none |

## Behaviour

- `New(downstream, rate)` rejects `rate <= 0` (`RateInvalid`) and nil
  downstream (`DownstreamNil`).
- `Write` atomically increments the counter; forwards only when
  `counter % rate == 0`. Dropped writes return `(0, nil)` — callers
  cannot tell the difference.
- Selection is **deterministic** (modulo on a monotonic counter), not
  probabilistic — every Nth call lands.
- `Flush` / `Close` are pure pass-throughs (the wrapper has no state to
  flush).

### `NewKeyed`

- **Zero is the documented sampler (ADR 0031).** `Window` 1 s, `First` 100,
  `Thereafter` 100, `MaxKeys` 1024 (`Default*`), `clock.System`, the
  level-and-message key, a `level.Fixed(level.Warn)` bypass. A negative bound
  is `RateInvalid`, a nil downstream `DownstreamNil`.
- **The window is lazy.** No goroutine, no ticker: a key's counter carries its
  window start and restarts on the first `Write` at or past `start + Window`,
  read on `Clock` (ADR 0090), so a `ManualClock` test advances it exactly.
- **The table is bounded.** At `MaxKeys` entries a new key first sweeps the
  keys whose window ended — at most once a window — and, when none did, is
  counted under ONE shared overflow counter: a message carrying an identifier
  cannot grow the table, and its records are still forwarded or dropped by a
  counter, never lost.
- **Drops are visible.** `Dropped()` (atomic) and `OnDrop(record)` on each
  drop; a dropped write returns `(0, nil)` as `New`'s does.
- **Cost.** ~100 ns a record, kept or dropped, 0 allocations once the key is
  known (the default key is a struct of level and message, never a
  concatenated string); `logger/BENCH.md` §7.

## Error catalogue — range 0.3.20.\*

Declared in `internal/core/observe/logger/middleware/sample` since ADR 0160 §2: this engine returns the sentinels below and declares none, so a test or a caller names them `coresample.X`.

| Code      | Sentinel        | Trigger |
|---|---|---|
| 0.3.20.1  | `RateInvalid`   | `New(..., rate)` with `rate <= 0` |
| 0.3.20.2  | `DownstreamNil` | `New(nil, ...)` |

## Do NOT

- Rely on which specific calls survive sampling for diagnostic purposes —
  the policy is "every Nth", not "the interesting ones".
- Compose `sample` outside `multi` if you need different rates per branch
  — wrap each branch sink individually.

## Verification

```
bazel test --config=race //internal/service/observe/logger/middleware/sample:sample_test
```
