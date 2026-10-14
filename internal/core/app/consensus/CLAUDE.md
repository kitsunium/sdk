<!-- updated: 2026-10-13T12:00:00Z -->
# internal/core/app/consensus/

## Purpose

The consensus **domain** (ADR 0152): every value a port speaks — node and
cluster identities, positions, entries, messages, snapshots, membership — and
the frozen ports `Replicated`, `StateMachine`, `LogStore`, `SnapshotStore`,
`Transport` and their siblings. The engine, its storage and its transport
live in `internal/service/app/consensus`; the facade is `pkg/v1/app/consensus`.

Code ranges: `0.2.57.*` (the domain's outcomes) and `0.3.92.*` (the shipped
storage and transport's), both declared here (ADR 0160).

## Files

| File | Holds |
|---|---|
| `design_gen.go` | the ports, written by kit from `design/app/consensus.yaml` |
| `codes_gen.go` | the codes and sentinels of both ranges, written by kit |
| `doc.go` | the package comment |
| `entry.go`, `identity.go`, `membership.go`, `message.go`, `snapshot.go`, `status.go` | the values |
| `position.go`, `stamp.go`, `timing_contract.go` | the values two or more of the files above hold as a field, one file named after each (KTN-STRUCT-COLOCATE) |
| `errors.go` | `exitCantCreate` (73), the exit code `StopReasonValue.ExitCode` returns for an unmarked stop; the sentinels' exit codes (65, 69, 73, 74, 75, 76, 78) are the design's `exit:` fields (ADR 0176) |

## Do NOT

- Add a method to a port: siblings are discovered by assertion (ADR 0039).
- Hand-edit `*_gen.go`: edit the design, run `kit gen`, then `make api`.
