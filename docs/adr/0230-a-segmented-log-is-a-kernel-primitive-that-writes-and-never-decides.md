# ADR 0230 — a segmented log is a kernel primitive that writes, and never decides

- **Status**: Accepted
- **Date**: 2026-11-12
- **Deciders**: SDK maintainers
- **Amended**: 2026-11-12 for the stream's compaction (#824), below.
- **Amends**: [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (the kernel holds what the domains were rewriting — here before a second domain rewrote it) and the rule of `internal/kernel/fs/CLAUDE.md` (a member of the family that writes). One new kernel package, `internal/kernel/fs/seglog` (`0.1.8.*`).
- **Related**: [ADR 0152](0152-a-write-is-committed-when-a-majority-holds-it-on-disk.md) D4, D16 and R8/R9 (the consensus file log this is extracted from), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (where a code is declared), issues [#820](https://github.com/kitsunium/sdk/issues/820) (this primitive), [#890](https://github.com/kitsunium/sdk/issues/890) (the `data/stream` program), [#821](https://github.com/kitsunium/sdk/issues/821) (its file engine, the first caller)

## Context

The SDK owns one measured, fuzzed, crash-recovering segmented log: the
consensus file log (`internal/service/app/consensus/filelog.go`, ADR 0152 D4).
Its record framing (length, CRC-32C, version, kind, end-of-batch flag, batch
sequence), its fixed-size zero-filled segments opened by a checkpoint, its one
batch under two syncs (#417), its recovery that classifies every hole before
it zeroes one (R8-02, R9-01, R9-03, #511), and its prefix drop by whole sealed
segments followed by a directory sync are the hardest code in the repository,
and every one of those rules was paid for by an incident.

The `data/stream` domain (#890) needs exactly that mechanism for its file
engine (#821). It cannot import it: `openFileLog` is unexported and bound to
the consensus data directory (its lock, stamp and stop markers), its records
carry a term, a hard state and `TruncateSuffix`, and a data engine importing
an app engine inverts the family order (ADR 0155). Rewriting it would
duplicate the code whose every line has a reason.

## Decision

1. **`internal/kernel/fs/seglog`**, stdlib-only, no domain word in a
   signature: `Open(dir, segBytes) (*Log, error)`, `Append(index, kind,
   payload)` buffering into the pending batch, `Sync()` writing it with the
   two-step end-record protocol, `Read(index)`, `Range(lo, hi, maxBytes,
   dst)`, `Kind(index)`, `DropBefore(index)`, `Bounds() (first, next)`,
   `Close()`; `DefaultSegmentBytes` 64 MiB, the consensus log's.
2. **The record keeps the framing and drops the vocabulary**:
   `length u32 | crc32c u32 | version u8 | kind u8 | end u8 | tag u8 | batch
   u64 | index u64 | payload`, 8-byte aligned. The `tag` is the caller's kind
   byte, stored and never read. No term, no hard state, no truncation of a
   suffix: an index once synced is never rewritten. A segment's checkpoint
   keeps the index its first entry takes, the log's first index and the next
   batch number; a full segment is sealed in a batch of its own between two
   batches, never inside one; a `DropBefore` is a record too, so the first
   index survives a restart.
3. **Recovery is the consensus log's, verbatim in its rules**: every segment
   scanned (in chunks, a sealed one up to its seal), the batch sequence held
   contiguous across the log, a record of an unknown version or kind refused
   before anything is classified (`VERSION_UNSUPPORTED`, never skipped), and
   a hole in the last segment a torn tail only when no valid record of a
   later batch and no valid end record of its own batch follows it — then it
   is zeroed and synced; any other hole is `DAMAGED` with the files left as
   they were. Everything recovery trusts is synced before `Open` returns.
4. **The primitive writes and never decides.** `internal/kernel/fs/` admitted
   only measurements and single primitives, "a package that decides, refuses
   or writes is a domain's". `seglog` writes — a log is nothing else — and is
   admitted because what it writes is a mechanism with no policy around it:
   it never quarantines (the consensus log writes a marker; the primitive
   returns `DAMAGED` and changes nothing), never retains by age or bytes,
   never locks its directory and never chooses a segment size beyond the
   default. Each of those is its caller's: the consensus data directory's
   markers and lock, the stream's retention policy.
5. **Codes `0.1.8.*`, declared in the kernel** like `ring`, `commit`, `pool`
   and `sema`: `DAMAGED`, `VERSION_UNSUPPORTED`,
   `OUT_OF_SEQUENCE`, `DROPPED`, `OUT_OF_RANGE`,
   `FAILED` (final after a failed write or sync — fsyncgate),
   `CLOSED`, `TOO_LARGE` (a payload above 1 GiB less a header).
6. **Consensus is NOT migrated here.** The primitive lands with the port of
   `FuzzScanSegment`, a fuzz target over a segment's tail (`FuzzOpenTail`)
   whose every input is either repaired as a torn tail — the synced records
   kept, every byte past the write position zero — or refused unchanged, and
   the consensus tail cases ported as `TestATailIsTornOrDamaged`. Moving
   `fileLog` onto it is a separate change, gated on the two fuzzers agreeing
   on the same inputs. Not published in `pkg/v1` (ADR 0159 §4): a program
   reaches it through `data/stream`.

## Consequences

- `data/stream`'s file engine (#821) builds on the kernel, and the
  consensus log can later shed its copy of the mechanism.
- Two copies of the recovery rules exist until the migration; a fix to one is
  a fix to both, and the two `CLAUDE.md` files say so.
- One more `fs/` member that writes: the family's rule now reads "measures,
  performs one primitive, or keeps a log, and never decides".

## Amendment — rewriting a record (#824), 2026-11-12

`data/stream`'s compaction must give back the bytes of the records it drops
without renumbering the survivors, which a log addressed by a contiguous
index cannot do by deleting them. `Log.Rewrite(below, edit)` is the one call
that changes a synced record, and it changes nothing a recovery reads:

1. **The caller edits, the log rewrites.** `edit` is offered every retained
   record below `below`, in index order, and returns a new kind and payload
   or leaves it. The log decides nothing about which: the decision —
   superseded value, tombstone past its window — is the stream's.
2. **Whole segments, published as the log publishes one.** A segment holding
   a changed record is written again whole into its `.tmp` — the same
   records, indexes, batch numbers and end flags; only the changed ones'
   kind and payload differ, the seal's offset field following its new place
   —, synced, renamed over the old file, the directory synced. Recovery
   already deletes every `.tmp` and reads either file the same way: no rule
   of the recovery changes, so the consensus copy needs no matching change
   (§6). A sealed segment shrinks to its records; the last keeps its size.
3. **Failures.** One before the rename leaves the segment as it was and the
   log usable; one from the rename on is final, as a failed sync is. The old
   file is closed before the rename, which Windows requires.

The record is still never truncated, and an index never moves: "no suffix
truncation" stands.

