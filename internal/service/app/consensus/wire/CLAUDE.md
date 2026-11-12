<!-- updated: 2026-11-12T12:00:00Z -->
# internal/service/app/consensus/wire/

## Purpose

The consensus domain's hand-written fixed layouts (ADR 0152 D4, D18): the
frame of a `MessageValue`, the 41-byte `TokenValue`, `SnapshotMetaValue`, `MembershipValue`,
the timing contract, the admin payload, and the CRC-32C body and chain sums.
They are not codec formats: two nodes must write the same bytes.
`EntryLen` is an entry's length in an append, its 33-byte header and its
payload: what the leader cuts a batch by, a store counting the payloads
alone (#683). `AppendSession` / `ParseSession` are the payload of an
`EntrySessionCommand`: the 16-byte session, the sequence (u64), then the
command (#918, ADR 0241).
`AppendHeader` and `AppendEntries` write an append's frame in two parts,
its `MessageHeaderLen` bytes of header and its entries body, header then
body being `AppendMessage`'s bytes: the leader encodes the body once per
Ready and shares it across followers (#912,
`TestAHeaderThenItsEntriesAreTheMessagesBytes`).

## Tests

`perf_fixtures_test.go` holds the fixtures of the frame codec's budgets in
`design/sdk.yaml` (ADR 0152 D15, #921): an append of 64 entries of 256 B
encoded into a warm buffer and decoded into a warm `MessageValue`, and one
chain value, each 0 allocations in the race-off lane (`gen_perf_test.go`).

## Do NOT

- Grow `ParseMessage`'s entry slice entry by entry: it is sized once for
  the count the frame states, after that count is checked against the
  body, so a frame of 64 entries costs one allocation for its entries, and
  their `Data` alias the frame (#1110, `TestAFollowerStepKeepsTheFramesPayloads`
  in the parent's alloc lane).
- Hand a stack buffer to `crc32.Checksum` or `crc32.Update` on a hot path:
  the update is called through a function variable, so the buffer moves to
  the heap, and `ChainSum` allocated at every entry the leader proposes and
  the follower verifies until its budget found it. `headerSum` sums the
  header over the table itself, the same CRC-32C
  (`TestChainSumIsTheCRC32COfItsHeader`, #921).
- Accept two encodings of one value in one version: a decoder refuses a
  non-canonical input, and the fuzz targets require every accepted frame
  to round-trip in the version it was written in. `SnapshotMeta` is read
  in version 1, version 2 (version 1's fields, then their CRC-32C) and
  version 3 (version 1's fields, the retired keys, then the CRC-32C) and
  version 4 (version 3's fields, the client sessions, then the CRC-32C,
  #918); the
  reader of version 2 shipped one release before the writer (#546, ADR
  0152 amendments of 2026-10-22 and 2026-10-23), and version 3 is written
  only at a cluster version every member announced (#648). No reader of
  version 3 shipped before its writer: a node of protocol 2 cannot decode
  it, nor stops on it, so the leader withholds such a snapshot from a peer
  that does not announce protocol 3 (#665). `export_test.go` writes each
  version for the suite.
- End a `SnapshotMeta` version from 2 on with anything but the CRC-32C of
  every byte before it: a reader that does not know the version tells by
  that sum a `.meta` a newer build wrote, which refuses its open, from one
  that rotted, which it skips (#665, the store's `newerMeta`).
- Judge an admin payload's `Change` in `ParseAdminAt`, or read the
  membership of an entry by its change kind: every reader takes the
  configuration from the payload whatever `Change` says, which is how
  `ChangeSnapshotLimit` (8) shipped with no protocol raise, a node of an
  earlier release adopting it as a follower (#677).
- Choose a layout's version by anything but the value or the entry it
  belongs to: `AppendSnapshotMeta` writes version 2 exactly when the
  meta's ClusterVersion is 2 (#546 phase 2), version 3 when it is 3
  (#648) and version 4 when it is 4 or more (#918), an admin payload carries Origin exactly when its entry's
  Version is AdminVersion2 or AdminVersion3 (#561) and the membership's
  retired keys exactly when it is AdminVersion3 (#648), so a node of a
  previous protocol never meets one before the raise.
- Write or accept a forward's trace context without `FlagTrace`, or on any
  kind but `MsgForward` and `MsgForwardAdmin`: the 25 bytes after its entry
  (trace id, span id, sampled 0 or 1) exist exactly beside the flag, which
  the driver sets only at cluster version 4, so a node of protocol 3 never
  meets them; anything else is FRAME_INVALID (#1115,
  `TestAForwardCarriesItsTraceContextOnlyBesideFlagTrace`, `FuzzParseMessage`).
