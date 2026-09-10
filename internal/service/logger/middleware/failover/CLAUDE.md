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

## Cost

**+9.4 ns over a bare sink when the first branch accepts, independent of how
long the chain is, zero allocations.** Failing over to the second branch adds
+11.9 ns. Measured in `internal/service/logger/BENCH.md` §1.

"Independent of chain length" is recent and is the point. `Write` used to open
with `make([]error, 0, len(s.chain))`, whose capacity is not a compile-time
constant, so from **three branches up** it heap-allocated on every record — on
the happy path, which returns on the first branch and never appends anything.
A five-branch chain cost **3.9× a two-branch chain while nothing was failing**:
the healthy path was taxed in proportion to how many fallbacks had been
configured for outages that were not occurring. The slate is now nil and grows
only when a branch actually fails. See BENCH.md §0.

## Do NOT

- Re-order branches at runtime — order encodes the failover priority.
- Treat `Exhausted` as a single error; unwrap it for per-branch causes.
- Pre-size the per-`Write` error slate again — a longer fallback chain must not
  cost the healthy path anything. `Flush` and `Close` keep theirs; they run
  once per sink lifetime, not once per record.

  This one is **guarded**, not merely written down:
  `TestChainDepthAddsNoAllocationOnTheHealthyPath` (`alloc_external_test.go`)
  compares the allocations of a write at depths 2/3/4/8 against the depth-1
  baseline and fails on any difference. It was checked against the defect it
  exists for — restoring the pre-sized slate takes the healthy write from 0 to
  1 alloc/op at depths 3, 4 and 8 while depth 2 still passes.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/failover:failover_test
# the depth guard carries //go:build !race and runs in exactly one lane:
bazel test --config=alloc //internal/service/logger/middleware/failover:failover_test
```
