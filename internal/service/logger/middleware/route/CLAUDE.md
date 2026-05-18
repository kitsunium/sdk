<!-- updated: 2026-05-18T14:30:00Z -->
# internal/service/logger/middleware/route/

## Purpose

Predicate-based dispatch `Sink` decorator. Each `Params{When, Sink}` entry
pairs a predicate with a downstream sink; on `Write`, the router walks
entries in declaration order and forwards the record to the **first
matching** sink. If no entry matches, an optional fallback sink receives
the write; without a fallback, `Write` returns `NoMatch`.

Use case: send `Error+` records to a remote alerting drain while keeping
`Info` records local on disk.

## Contents

| File | Role |
|---|---|
| `router_sink.go`        | `routerSink` + `New` + `Write` / `Flush` / `Close` |
| `router_sink_params.go` | `Params{When, Sink}` + `Predicate` type + `LevelAtLeast` helper |
| `codes.go`, `errors.go` | sentinels — range 0.3.18.\* |

## Behaviour

- `New(fallback, entries...)` defensively copies the entries slice. Any
  entry with `When == nil` OR `Sink == nil` is **silently dropped** (the
  documented contract — partial entries are not errors).
- `Predicate` is `func(RecordEvent) bool`. `LevelAtLeast(min)` is the
  shipped helper; callers wire arbitrary predicates for routing.
- `Write` forwards to the first matching `Sink`. Misses fall through to
  the fallback, then to `NoMatch`.
- `Flush` / `Close` walk every entry + the fallback and aggregate per-sink
  errors via `errors.Join`.

## Error catalogue — range 0.3.18.\*

| Code      | Sentinel  | Trigger |
|---|---|---|
| 0.3.18.1  | `NoMatch` | no entry matched AND no fallback was configured |

## Do NOT

- Pass partial `Params` (nil `When` or nil `Sink`) expecting them to error
  — they are dropped at construction.
- Rely on side-effects from predicate evaluation; predicates may be called
  many times under concurrent producers.

## Verification

```
bazel test --config=race //internal/service/logger/middleware/route:route_test
```
