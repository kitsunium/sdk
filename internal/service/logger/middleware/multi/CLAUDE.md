<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/multi/

## Purpose

Fan-out `Sink` decorator. Broadcasts every `Write` to a fixed list of
downstream branches in declaration order. A single branch failure does
**not** short-circuit the fan-out — every branch sees the payload, and
the per-branch errors are aggregated via `errors.Join` wrapped under the
`FanoutWriteFailed` sentinel.

Use case: emit to console + file + remote drain simultaneously with
independent failure modes per branch.

## Contents

| File | Role |
|---|---|
| `multi.go` | `fanoutSink` + `New` + `Write` / `Flush` / `Close` |
| `codes.go`, `errors.go` | sentinels — range 0.3.16.\* |

## Behaviour

- `New(branches...)` defensively copies the slice and silently skips nil
  entries. A nil/empty branches slate is allowed — the resulting sink is
  a no-op (every `Write` returns `(0, nil)`).
- `Write` tracks the latest successful branch's byte count as `n`. Errors
  from any branch are collected; if any failed, `Write` returns
  `(n, errs.Wrap(errors.Join(...), FanoutWriteFailed))`.
- `Flush` / `Close` mirror the same aggregation policy.

## Error catalogue — range 0.3.16.\*

| Code      | Sentinel             | Trigger |
|---|---|---|
| 0.3.16.1  | `FanoutWriteFailed`  | one or more branches' `Write` failed; wraps `errors.Join` of causes |

## Cost

**8.4 ns per destination plus 8.8 ns of fixed overhead, zero allocations at any
width** — measured in `internal/service/logger/BENCH.md` §1, which benchmarks
this package against a shared discard control. Linear from 1 to 8 branches.

That linearity is recent. `Write` used to open with
`make([]error, 0, len(s.branches))`, and because the capacity is not a constant
the compiler could only keep it on the stack while it fit its implicit budget —
so at **three or more branches** every `Write` heap-allocated, on the healthy
path where no branch ever fails and the slice stays empty. A caller crossed that
cliff by adding a third log destination, and it broke the SDK's
one-allocation-per-emit claim: a `recover`→`failover(4)`→`multi(4)` stack
measured **3 allocs/op**. The slice is now declared nil and appended to only on
failure — the shape the sibling `tee` has always used. See BENCH.md §0 for the
pprof line and the before/after.

## Do NOT

- Mutate the branches slice after construction — the fanout sink owns it.
- Treat the returned byte count as the sum across branches; it is the
  count from the last successful branch only.
- Pre-size the per-`Write` error slate again. `Write` runs once per log record;
  a capacity that is not a compile-time constant costs an allocation on every
  record to pre-book space for failures that are not happening. `Flush` and
  `Close` keep theirs — they run once per sink lifetime.

  This one is **guarded**, not merely written down: `TestFanoutWidthAddsNoAllocation`
  (`pkg/v1/logger/fanout_integration_test.go`) compares the allocations of an
  emit at widths 2/3/4/8 against the width-1 baseline and fails on any
  difference. It was checked against the defect it exists for — restoring the
  pre-sized slate makes widths 3, 4 and 8 fail while width 2 still passes.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/multi:multi_test
# the width guard lives at the public edge and runs race-off:
bazel test --config=alloc //pkg/v1/logger:logger_test
```
