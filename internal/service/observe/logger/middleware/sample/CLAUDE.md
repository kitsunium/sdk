<!-- updated: 2026-09-28T19:19:15Z -->
# internal/service/observe/logger/middleware/sample/

## Purpose

1-of-N rate-limiter `Sink` decorator. Every Nth `Write` is forwarded to
the downstream sink; the rest are silently dropped. A single
`atomic.Uint64` counter keeps concurrent producers correct without a
mutex on the hot path.

Use case: emit verbose `Debug` records at a 1/100 rate to a remote drain
while keeping the local console stream intact (compose with `multi` for
per-branch policies).

## Contents

| File | Role |
|---|---|
| `sample_sink.go` | `sampleSink` + `New` + `Write` / `Flush` / `Close` |
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
