<!-- updated: 2026-10-13T12:00:00Z -->
# internal/service/app/consensus/wire/

## Purpose

The consensus domain's hand-written fixed layouts (ADR 0152 D4, D18): the
frame of a `MessageValue`, the 41-byte `TokenValue`, `SnapshotMetaValue`, `MembershipValue`,
the timing contract, the admin payload, and the CRC-32C body and chain sums.
They are not codec formats: two nodes must write the same bytes.

## Do NOT

- Accept two encodings of one value: a decoder refuses a non-canonical
  input, and the fuzz targets require every accepted frame to round-trip.
