<!-- updated: 2026-10-23T02:00:00Z -->
# internal/service/app/consensus/wire/

## Purpose

The consensus domain's hand-written fixed layouts (ADR 0152 D4, D18): the
frame of a `MessageValue`, the 41-byte `TokenValue`, `SnapshotMetaValue`, `MembershipValue`,
the timing contract, the admin payload, and the CRC-32C body and chain sums.
They are not codec formats: two nodes must write the same bytes.

## Do NOT

- Accept two encodings of one value in one version: a decoder refuses a
  non-canonical input, and the fuzz targets require every accepted frame
  to round-trip in the version it was written in. `SnapshotMeta` is read
  in version 1 and version 2 (version 1's fields, then their CRC-32C); the
  reader shipped one release before the writer (#546, ADR 0152 amendments
  of 2026-10-22 and 2026-10-23). `export_test.go` writes each version for
  the suite.
- Choose a layout's version by anything but the value or the entry it
  belongs to: `AppendSnapshotMeta` writes version 2 exactly when the
  meta's ClusterVersion is 2 or more (#546 phase 2), an admin payload
  carries Origin exactly when its entry's Version is AdminVersion2 (#561),
  so a node of protocol 1 never meets one before the raise.
