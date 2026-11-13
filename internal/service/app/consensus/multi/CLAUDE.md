<!-- updated: 2026-11-12T00:00:00Z -->
# internal/service/app/consensus/multi/

## Purpose

A coalescing proposer on top of the consensus domain's public port, with no
change to the engine (#926, ADR 0152 D6, D19). `Packer` packs the commands
its callers propose at one moment into one length-prefixed body and issues
ONE `Replicated.Propose`; `Machine{Inner}` on every node splits that
body and applies the commands, in order, through ONE call of
`Inner.ApplyBatch`; the Packer answers each caller with a `ResultValue`: the
entry's token, the command's index in it (`Sub`) and its own result. An
entry carries a header, a slot, a CRC-32C and an apply result whatever its
size, so for commands of a few dozen bytes the entry count, not the bytes,
bounds the leader's turn and the follower's parse: `BENCH.md` measures 3.6
to 4.5 times the commands per second at 1 000 callers of 64-byte commands.

It declares no code: it raises the core's — `FRAME_INVALID` for a body that
does not parse, `ENTRY_TOO_LARGE` for a command that cannot fit an entry
alone, `RESULT_TYPE_MISMATCH` for an entry whose result is not one per
command (the entry committed), `OUTCOME_UNKNOWN` for a caller that left once
its command was packed, `NODE_STOPPED` for a closed Packer.

## Body

`1` (the version), the uvarint count of commands, then each command as its
uvarint length and its bytes (`appendBody`, `splitBody`). The entry's
`BodySum` still covers the whole body; a sub-entry carries the command's own
`BodySum` (the per-command digest a client session needs, G8) and a zero
`ChainSum`. A count above the bytes left, a length past them, trailing bytes
or another version are `FRAME_INVALID`, decided the same on every node.

## Do NOT

- Add a linger timer: a command proposed while fewer than `MaxInFlight`
  entries are in flight leaves at once (D6). The default is one in flight:
  measured, more in flight packed fewer commands an entry and did no better
  (`BENCH.md`).
- Read a caller's buffer once its request is taken: `take` packs it into a
  fresh body under `mu`, and from then on a caller that leaves may reuse it
  (`TestACancelledCallerIsAnsweredByWhetherItsCommandLeft`).
- Answer a caller that left before its command was taken anything but
  `ctx.Err()`: it is removed from the queue and never proposed, as the
  engine's `submit` answers a proposal the loop never had.
- Propose a command whole to a node running a `Machine`: it is not a
  packed body and is answered `FRAME_INVALID`.
- Run `Machine.Apply` concurrently: it reuses its scratch, and the
  engine calls it from the apply goroutine alone (the test double serialises
  it too).

## Tests

| What | Where |
|---|---|
| body round trip, every refusal | `body_internal_test.go` |
| packing, bounds, cancellation, close, errors | `packer_external_test.go` (a `Replicated` double) |
| a packed entry on every node, in caller order; the linearizability client over `Machine`, its register checker refusing a stale read first | `cluster_external_test.go` (three nodes over the memory network) |
| the gate of #926 | `multi_bench_test.go`, `BENCH.md` |
