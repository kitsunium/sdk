<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/sample/

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
| `codes.go`, `errors.go` | sentinels — range 0.3.20.\* |

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
bazel test --config=race //internal/service/logger/middleware/sample:sample_test
```

## Accepted audit findings

- Deferred/accepted low+info audit findings (V36) are recorded in `.claude/contexts/sdk-audit-2026-06-03-accepted.yaml` (2026-06-03 close-out). Each is a deliberate decision or deferred change, not an open bug.
