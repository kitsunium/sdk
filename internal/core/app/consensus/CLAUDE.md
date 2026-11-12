<!-- updated: 2026-11-12T12:00:00Z -->
# internal/core/app/consensus/

## Purpose

The consensus **domain** (ADR 0152): every value a port speaks — node and
cluster identities, positions, entries, messages, snapshots, membership — and
the frozen ports `Replicated`, `StateMachine`, `LogStore`, `SnapshotStore`,
`Transport` and their siblings — `LeadershipWatcher` and, beside it,
`MembershipWatcher` (#915), the configuration pushed apart from `StatusValue`,
which stays frozen, and `SessionProposer` (ADR 0241: a `SessionID`, its
`SessionValue` record, `EntrySessionCommand` of protocol version 4, announced
by `FlagProtocol4` as #919's heartbeats and #1115's trace are,
`SnapshotMetaValue.Sessions`, `SESSION_DIGEST_MISMATCH` `0.2.57.26` and
`SESSION_EXPIRED` `0.2.57.27`). The engine, its storage and its transport
live in `internal/service/app/consensus`; the facade is `pkg/v1/app/consensus`.

Code ranges: `0.2.57.*` (the domain's outcomes) and `0.3.92.*` (the shipped
storage and transport's), both declared here (ADR 0160).

## Files

| File | Holds |
|---|---|
| `gen_ports.go` | the ports, written by kit from `design/app/consensus.yaml` |
| `gen_codes.go` | the codes and sentinels of both ranges, written by kit |
| `doc.go` | the package comment |
| `entry.go`, `identity.go`, `membership.go`, `message.go`, `snapshot.go`, `status.go` | the values |
| `position.go`, `stamp.go`, `timing_contract.go` | the values two or more of the files above hold as a field, one file named after each (KTN-STRUCT-COLOCATE) |
| `errors.go` | `exitCantCreate` (73), the exit code `StopReasonValue.ExitCode` returns for an unmarked stop; the sentinels' exit codes (65, 69, 73, 74, 75, 76, 78) are the design's `exit:` fields (ADR 0176) |

## Do NOT

- Add a method to a port: siblings are discovered by assertion (ADR 0039).
  `SessionProposer` sits beside `Replicated`, never inside it.
- Let `Known` admit a kind without the protocol version that writes it:
  `EntrySessionCommand` is appended only once the cluster version is 4, so
  a node of protocol 3, whose `Known` stops at `EntryTransfer`, never
  meets it (D18, #918).
- Hand-edit `*_gen.go`: edit the design, run `kit gen`, then `make api`.
