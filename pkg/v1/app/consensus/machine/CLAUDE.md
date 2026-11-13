<!-- updated: 2026-11-12T12:00:00Z -->
# pkg/v1/app/consensus/machine/

## Purpose

Public facade of the consensus domain's state machines (ADR 0152 D3, #916),
written by kit into `gen_facade.go` from `design/app/consensus.yaml`: the
aliases `SQL`, `SQLConfig`, `KV`, `KVResult`, the constant
`DefaultPositionTable` and the forwarders `NewSQL`, `NewKV`, `KVGet`,
`KVPut`, `KVCompareAndSwap` onto `internal/service/app/consensus/machine`.
A package of its own so that a program of the engine alone links no
`data/sql` port.

## Conventions

- **Type aliases, not new types.**
- README written by `tools/genindex` from `docs/api`, with `ExampleKV`
  (`machine_external_test.go`); edit the design, run `kit gen`, then
  `make api` and `make docs-readme`.

## Do NOT

- Hand-edit `gen_facade.go` or `README.md`.
