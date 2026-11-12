<!-- updated: 2026-11-12T12:00:00Z -->
# pkg/v1/app/consensus/

## Purpose

Public facade of the consensus domain (ADR 0152 D14), written by kit into
`gen_facade.go` from `design/app/consensus.yaml`: aliases for every type a
published port speaks, `NewNode`, `Bootstrap`, the data directory and its
offline operations — `ImportObserved` and `OpenSnapshot` among them, what a
non-member observer rebuilds from beside `Node.ReadCommitted`, and
`Preflight`, the disk's fdatasync measured against the timing contract —, the
incremental backup (`Node.ExportBackup`, `Node.AppendBackup`,
`VerifyBackup`, `RestoreBackup`, its recovery point in the package doc's
Backup section, #927),
`NewTCPTransport` and `NewTCPTransportFromPEM`, the
memory doubles, the wire codecs and the sentinels of both ranges. The
`LeadershipWatcher` and `MembershipWatcher` siblings are aliased too: a
product finds them by type assertion on its `*Node` (ADR 0039, #915). The
state machines (`NewSQL`, `NewKV`) are `machine/`'s facade (#916).

## Conventions

- **Type aliases, not new types.**
- **After OUTCOME_UNKNOWN, never retry `Propose` blindly**: read through
  `Barrier`. A command under a client session (`Node.OpenSession`,
  `Node.ProposeSession`, the `SessionProposer` alias, ADR 0241) is retried
  with the same arguments; the package doc's section on it is the
  consumer's account.
- README written by `tools/genindex` from `docs/api`; edit the design, run
  `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Hand-edit `gen_facade.go`.
