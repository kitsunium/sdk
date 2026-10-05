# framework/telemetry — the port a product reports on

## Purpose

ADR 0149: a port of numbers (`Emitter.Emit(*Event)`, zero allocations), `Nop`
by default, and the `Exporter` that hands events to a tool attached on a
private socket (`pkg/v1/proc/ipc`, ADR 0148). The framework's `App` reports every
span and phase change on it when `KIT_TELEMETRY` names a socket at the start
(`framework/kit/telemetry.go`).

## Contents

| File | Role |
|---|---|
| `doc.go` | the package comment — kit writes it from the design (ADR 0167) |
| `decl_gen.go` | written by kit gen from the design (ADR 0170): the declarations of `Kind`, `Op`, `Outcome`, `NodeRef`, `Event`, `HelloValue`, `ExporterConfig`, `Exporter` and `Emitter` — each struct with every field, unexported ones included; `Exporter.Ref` and `OpOf`, each one call of its unexported body, measured to inline with the body inlined into it; the assertion `Exporter → Emitter`. Every body stays hand-written, in the files this document names — each wrapper's under its unexported name |
| `telemetry.go` | `Kind`, `Op` + `OpNames` + `OpOf`, `Outcome`, `NodeRef`, `Event`, `Nop`, `RecordSize`, `encode` / `Decode` (64 bytes, little-endian, fixed offsets); the exporter's constants and types — `Protocol`, the buffer bounds, `HelloValue`, `ExporterConfig`, `Exporter`, the ring (`slot`, `ring`) and the padded `counter` — live here too, next to `Event`, as ktn-linter's co-location rule asks |
| `exporter.go` | the exporter's behaviour: `NewExporter(*ExporterConfig)`, `Ref`, `Instance`, `Emit` — the bounded ring, one CAS per event, drop and count when full —, `Start`, `Stop`, `Dropped`, `Sent`, the handshake line; a client whose close fails is logged, never returned |
| `telemetry_interface.go` | `Emitter` (the port a product emits to), and the exporter's narrow views of its listener (`accepter`) and of a client (`writer`) |
| `codes_gen.go` | `0.4.3.1` `MISCONFIGURED`, `0.4.3.2` `RUNNING` — written by kit gen from `design/framework/telemetry.yaml` (ADR 0164) |
| `BENCH.md` | `Emit` 11 ns / 0 allocs, `Nop` 3 ns |

## Rules

- **Numbers only.** An `Event` gains no string field, ever: the node table in
  the handshake is the only text, and it holds IDs the grammar accepts. A
  field a person's data could travel in is what the design excludes.
- **`Emit` never allocates and never blocks.** `TestEmitAllocatesNothing`
  (`//go:build !race`) runs in the alloc lane — `//framework/telemetry:telemetry_test`
  is in `tools/alloc-lane-targets.txt`.
- **The exporter never reads a client.** It writes the handshake, then
  records; a client that does not read within 250 ms is dropped.
- **An `Op` is appended, never renumbered.** A reader keeps the table from
  the handshake, but a record's byte is an index.

## Verification

```sh
GOWORK=off go -C framework test -race ./telemetry/
GOWORK=off go -C framework test -run='^$' -bench=. -benchmem ./telemetry/
```
