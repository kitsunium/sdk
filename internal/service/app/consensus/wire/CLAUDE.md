<!-- updated: 2026-10-25T17:00:00Z -->
# internal/service/app/consensus/wire/

## Purpose

The consensus domain's hand-written fixed layouts (ADR 0152 D4, D18): the
frame of a `MessageValue`, the 41-byte `TokenValue`, `SnapshotMetaValue`, `MembershipValue`,
the timing contract, the admin payload, and the CRC-32C body and chain sums.
They are not codec formats: two nodes must write the same bytes.
`EntryLen` is an entry's length in an append, its 33-byte header and its
payload: what the leader cuts a batch by, a store counting the payloads
alone (#683).

## Do NOT

- Accept two encodings of one value in one version: a decoder refuses a
  non-canonical input, and the fuzz targets require every accepted frame
  to round-trip in the version it was written in. `SnapshotMeta` is read
  in version 1, version 2 (version 1's fields, then their CRC-32C) and
  version 3 (version 1's fields, the retired keys, then the CRC-32C); the
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
  meta's ClusterVersion is 2 (#546 phase 2) and version 3 when it is 3 or
  more (#648), an admin payload carries Origin exactly when its entry's
  Version is AdminVersion2 or AdminVersion3 (#561) and the membership's
  retired keys exactly when it is AdminVersion3 (#648), so a node of a
  previous protocol never meets one before the raise.
