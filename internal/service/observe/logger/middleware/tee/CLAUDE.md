<!-- updated: 2026-10-05T00:00:00Z -->
# internal/service/observe/logger/middleware/tee/

## Purpose

Fan-out + dead-letter spill `Sink` decorator. Delivers each record to every
primary sink and, **only when every primary fails**, routes that record to an
optional spill (dead-letter) sink so it is not silently lost (ADR 0014 §D5).

`tee` owns exactly one seam: the dead-letter (spill) path. It deliberately
does **not** re-implement plain fan-out (that is `multi`) or ordered fallback
(that is `failover`). The spill seam is what neither covers.

Durable retry and backoff are out of scope — the spill sink is a dead-letter
seam, not a retry queue. Compose retry behind the spill sink.

## Contents

| File | Role |
|---|---|
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `Config` and `TeeSink` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names |
| `tee.go` | `TeeSink` + `NewTeeSink` (returns `*TeeSink`) + `Write` / `Flush` / `Close` + wrap helpers |
| `internal/core/observe/logger/middleware/tee` | its sentinels — range 0.3.29.\* — declared in the core mirror since ADR 0160; this package declares none |
| `doc.go` | the package comment — kit writes it from the design (ADR 0167): scope, non-goals, concurrency |

## Spill semantics

- Every primary receives the record.
- A record accepted by **at least one** primary is never spilled.
- A record rejected by **all** primaries is routed to the spill sink.
- All-primaries-failed surfaces `CodeTeeAllBranchesFailed` (wrapping the
  joined primary causes). If the spill also fails, `CodeSpillFailed` is
  surfaced instead.
- A nil spill sink disables the dead-letter seam; all-failed then surfaces
  `CodeTeeAllBranchesFailed` alone.
- `Flush` / `Close` forward to every primary and the spill sink, aggregating
  per-branch errors via `errors.Join`.

## Error catalogue — range 0.3.29.\*

Declared in `internal/core/observe/logger/middleware/tee` since ADR 0160 §2: this engine returns the sentinels below and declares none, so a test or a caller names them `coretee.X`.

| Code | Sentinel | Trigger |
|---|---|---|
| 0.3.29.1 | `AllBranchesFailed` | every primary's `Write` failed; wraps `errors.Join` of causes |
| 0.3.29.2 | `SpillFailed` | the dead-letter sink's `Write` failed for an all-failed record |

## Concurrency

Safe for concurrent producers when the primary and spill sinks are. The
`TeeSink` holds no mutable per-record state.

## Do NOT

- Re-implement `multi` (plain fan-out) or `failover` (ordered fallback) here.
- Treat the spill sink as a retry queue — it is a dead-letter seam only.

## Verification

```
bazel test --config=race //internal/service/observe/logger/middleware/tee:tee_test
```
