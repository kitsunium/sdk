<!-- updated: 2026-11-12T00:00:00Z -->
# internal/kernel/fs/seglog/

## Purpose

A durable, append-only **segmented log** of records addressed by a
contiguous `uint64` index (#820, ADR 0230): the mechanism of the consensus
file log (`internal/service/app/consensus/filelog.go`, ADR 0152 D4)
extracted without its vocabulary — no term, no hard state, no truncation of a
suffix — so that `data/stream`'s file engine (#821) and, once migrated, the
consensus log build on one copy. A kernel primitive (stdlib-only AND
generic). Code range `0.1.8.*`. Not published in `pkg/v1` (ADR 0159 §4).

## Contents

| File | Surface |
|---|---|
| `record.go` | the record framing (`appendRecord`, `parseRecord`, `sealedBody`), the checkpoint payload, `DefaultSegmentBytes` |
| `log.go` | `Log`, `Open`, `Append`, `Sync` (the two-step batch), `Read`, `Range`, `Kind`, `DropBefore`, `Bounds`, `Close` |
| `rewrite.go` | `Rewrite` (#824, ADR 0230's amendment): a segment holding a record the caller's edit changed parsed, re-encoded with the same indexes, batches and end flags, written to its `.tmp`, synced, renamed over the old file (closed first, for Windows), the directory synced; the slots moved; the segment being written grows when an edit lengthened its records, so the seal of its next rotation still fits |
| `recover.go` | the recovery: scan in chunks, batch sequence, unknown layouts, `classifyTail`, replay, sync at open, tail positioning |
| `segment.go` | segment publication (zero-filled `.tmp`, checkpoint, sync, rename, directory sync), the background preparation, rotation, prefix drop |
| `errors.go` | `failed` / `damaged` / `outside`, the fields every error carries |
| `sync_linux.go`, `sync_other.go`, `syncdir_*.go` | `fdatasync(2)` on Linux, `os.File.Sync` elsewhere (`F_FULLFSYNC` on darwin); a directory sync, a no-op on Windows |
| `gen_codes.go` | the eight codes and sentinels, written by kit gen from `design/kernel/fs.yaml` |
| `tail_internal_test.go` | `TestATailIsTornOrDamaged`: the consensus tail cases ported (see below), plus the seal-without-its-next-segment, end-record and sealed-scan tests |
| `fuzz_internal_test.go` | `FuzzOpenTail`, `FuzzScanSegment` (ported), `FuzzParseRecord` |
| `rewrite_external_test.go` | a rewrite read back changed and every other record unchanged, before and after a reopen, a sealed segment shrunk, the last one still appended to; an edit that changes nothing writes nothing and is never offered a dropped record; a pending batch synced first; a last segment an edit grew still sealed at its rotation and reopened |
| `seglog_bench_test.go` | `AppendSync` for 1, 16 and 256 records, `Range` — `BENCH.md` |

## The format

```
length u32 | crc32c u32 | version u8 | kind u8 | end u8 | tag u8 | batch u64 | index u64 | payload | pad to 8
```

Kinds: entry (1, `tag` the caller's kind byte), checkpoint (2, opens every
segment: next index, first index, next batch), seal (3, closes a segment in
a synced batch of its own), drop (4, a `DropBefore`'s new first index).
Segments are `seg-%016x`, fixed-size and zero-filled; the next one is
prepared as a `.tmp` in the background and published by rename then a
directory sync; recovery deletes any `.tmp`.

## Conventions

- **One batch, two syncs.** `Sync` writes every record but the last, syncs,
  then the last one flagged end-of-batch, syncs (#417's protocol). A batch of
  one record is one sync.
- **Classify, then repair.** A hole in the last segment is a torn tail only
  when no valid record of a later batch and no valid end record of its own
  batch follow it: then it is zeroed and synced. Anything else is
  `DAMAGED` with the files untouched; a sealed record of an unknown
  version or kind is `VERSION_UNSUPPORTED`, never skipped.
- **Final after a failure.** A failed write, sync or segment drop stores
  `l.failed`; every later call returns it (fsyncgate).
- **Readers never wait for the disk.** A sync releases `mu` across its I/O;
  pending records are read from the pending buffer, synced ones by `pread`.
  `Range` reads a run of contiguous records with one `pread`.
- **Retention by whole segments.** `DropBefore` records the first index in a
  drop record, syncs, then unlinks every sealed segment whose successor's
  checkpoint starts at or below it, and syncs the directory.
- **A rewrite changes no index.** `Rewrite` is the one call that changes a
  synced record: kind and payload only, the segment written whole and
  published by rename, so recovery reads the new file as it read the old.
  Not benchmarked here: its cost is the stream's compaction, measured in
  `internal/service/data/stream/BENCH.md`.

## The ported tail cases

`TestATailIsTornOrDamaged` holds, on the primitive, the eight shapes of
`filelog_internal_test.go`: the clean write position, an unfinished highest
batch, garbage after a complete batch and a length near 2^31 (all repaired);
a hole before a later batch, damage inside a batch whose end record is
valid, a newer layout's record behind a hole and a hole in an earlier
segment (all refused, unchanged). `FuzzOpenTail` seeds itself with the same
shapes and holds every input to one of the two verdicts. The three fuzz
targets run beyond their seeds in the nightly consensus fuzz lane
(`scripts/ci/consensus-fuzz.sh`), eight minutes each.

## Do NOT

- Add a policy: no quarantine marker, no directory lock, no retention by age
  or bytes. They are the callers' (ADR 0230 §4).
- Rewrite a synced record except through `Rewrite`, and never its index,
  batch or end flag. There is no suffix truncation here.
- Change the framing, the batch protocol or the recovery rules without the
  same change in the consensus file log until it is migrated (ADR 0230 §6).

## Verification

```
cd internal && go test -race -count=1 ./kernel/fs/seglog/
cd internal && go test -run '^$' -fuzz '^FuzzOpenTail$' -fuzztime 60s ./kernel/fs/seglog/
cd internal && go test -run '^$' -bench . -benchmem -cpu 1 -count 5 ./kernel/fs/seglog/
```
