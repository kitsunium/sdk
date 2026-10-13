<!-- updated: 2026-10-13T12:00:00Z -->
# pkg/v1/app/consensus/

## Purpose

Public facade of the consensus domain (ADR 0152 D14), written by kit into
`facade_gen.go` from `design/app/consensus.yaml`: aliases for every type a
published port speaks, `NewRaft`, `Bootstrap`, the data directory and its
offline operations, `NewTCPTransport` and `NewTCPTransportFromPEM`, the
memory doubles, the wire codecs and the sentinels of both ranges.

## Conventions

- **Type aliases, not new types.**
- **After OUTCOME_UNKNOWN, never retry blindly**: read through `Barrier`.
- README written by `tools/genindex` from `docs/api`; edit the design, run
  `kit gen`, then `make api` and `make docs-readme`.

## Do NOT

- Hand-edit `facade_gen.go`.
