<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/failover/

## Purpose

Sequential-retry `Sink` decorator. Tries downstream sinks in declaration
order; the **first success short-circuits** and returns `nil`. When every
branch fails, `Write` returns `Exhausted` wrapping `errors.Join` of the
per-sink failures so callers can `errors.Is` every cause.

Use case: emit to a remote collector with a local file as fallback so
logs survive a network outage.

## Contents

| File | Role |
|---|---|
| `failover_sink.go` | `failoverSink` + `New` + `Write` / `Flush` / `Close` |
| `codes.go`, `errors.go` | sentinels — range 0.3.19.\* |

## Behaviour

- `New(branches...)` defensively copies the slice and silently skips nil
  entries; an empty post-filter chain returns `Empty`.
- `Write` walks the chain in order; first non-error wins (its byte count
  is returned). Every branch failure is collected into the `errors.Join`
  set surfaced by `Exhausted`.
- `Flush` and `Close` forward to every branch unconditionally — they never
  short-circuit on a failure — and aggregate per-branch errors via
  `errors.Join`. Callers see every cause via `errors.Is`.

## Error catalogue — range 0.3.19.\*

| Code      | Sentinel    | Trigger |
|---|---|---|
| 0.3.19.1  | `Exhausted` | every branch's `Write` failed; wraps `errors.Join` of causes |
| 0.3.19.2  | `Empty`     | `New()` post-filter chain is empty (no non-nil branches) |

## Do NOT

- Re-order branches at runtime — order encodes the failover priority.
- Treat `Exhausted` as a single error; unwrap it for per-branch causes.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/failover:failover_test
```
