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

## Do NOT

- Mutate the branches slice after construction — the fanout sink owns it.
- Treat the returned byte count as the sum across branches; it is the
  count from the last successful branch only.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/multi:multi_test
```
