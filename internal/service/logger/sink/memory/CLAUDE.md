# internal/service/logger/sink/memory/

## Purpose

In-memory terminal `Sink`: buffers a defensive snapshot of every received
`core/logger.RecordEvent` in a mutex-guarded slice, for tests that assert on
what was logged (level, message, attrs) without parsing an encoder's byte
output. Mirrors apex/log's memory handler.

## Contents

| File | Role |
|---|---|
| `memory.go`            | `Memory` struct + `NewMemory` + `Write` / `Flush` / `Close` / `Records` / `Len` / `Reset` |
| `memory_compliance.go` | compile-time `var _ core/logger.Sink = (*Memory)(nil)` |
| `doc.go`               | package doc |

## Why this shape

- **Real Sink port.** `Memory` implements `Write(ctx, RecordEvent, []byte)
  (int, error)` / `Flush(ctx) error` / `Close() error`. `Write` ignores the
  encoder bytes `p` (it retains the structured record) but returns `len(p)`
  so the Handler's byte accounting stays consistent.
- **Defensive snapshot.** `Write` stores a struct copy of the record with
  `slices.Clone(r.Attrs)`, so later mutation of the caller's `Attrs` slice
  cannot corrupt recorded history.
- **RWMutex-guarded.** `Write` / `Reset` take the write lock; `Records` /
  `Len` take the read lock so concurrent assertions never block each other.
  `Len` returns the accepted-Write count (a second field beyond the guarded
  slice).
- **Copy-out reads.** `Records` returns `slices.Clone` of the buffer so
  callers can iterate while other goroutines keep writing; `nil` stays `nil`.
- **No-op lifecycle.** `Flush` / `Close` hold no resources; `Close` leaves
  records readable so tests can assert on a closed sink. `Write` honours
  `ctx.Err()` and surfaces the cancellation cause directly (no error code is
  minted — the package stays stdlib-only).

## Exported struct, not the interface

Unlike console/file/syslog (which return the `Sink` interface), `NewMemory`
returns the concrete `*Memory` because `Records` / `Reset` are inspection
helpers outside the `Sink` port. `pkg/v1/logger` re-exports it as
`MemorySink` with `NewMemorySink`.

## Layer

service → depends only on `core/logger` (Sink, RecordEvent, AttrValue).
Stdlib otherwise.

## Verification

```
bazel test --config=race //internal/service/logger/sink/memory:memory_test
```
