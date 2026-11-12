# internal/core/data/stream/

## Purpose

The contract of the SDK's durable, partitioned, offset-addressed log — its
"internal Kafka" (ADR 0231, kitsunium/sdk#890): the `Stream` port a producer
appends batches of records to and any number of readers read, each partition
from an offset of the reader's own; the values that travel through it; the
policy every engine enforces; the one key-to-partition placement every engine
applies; and every error code of the domain, its engines' included (ADR 0160).
No engine lives here: the memory engine and the conformance suite every
engine runs are `internal/service/data/stream`'s (#819); the file engine over
`kernel/fs/seglog` (#821), the SQL engine (#822) and the subscriber (#823)
arrive there one milestone each.

## Contents

| File | What it holds |
|---|---|
| `gen_ports.go` | written by kit from `design/data/stream.yaml`: `Stream` (FROZEN at four), `Waker`, `Seeker`, `Trimmer`, `Compactor`, `Committer` |
| `gen_codes.go` | written by kit: `0.2.38.*` (the core's nine) and `0.3.94.*` (the engines' and the subscriber's four) with their sentinels |
| `record.go` | `Offset`, `RecordValue`, `EntryValue`, `AppendValue`, `BoundsValue` |
| `policy.go` | `PolicyValue`, `Validate`, `Normalized`, `DefaultMaxRecordBytes`, `DefaultSegmentBytes` |
| `partition.go` | `PartitionOf` — 32-bit FNV-1a modulo the partition count, inlined so it allocates nothing |
| `doc.go`, `gen_api_test.go` | written by kit: the package comment and the pins |
| `*_external_test.go` | the policy's refusals by field, `PartitionOf` held to `hash/fnv`, every sentinel's value, reason and statuses |

## The decisions, and where each one is argued

- **The frontier with `queue`** (ADR 0231 §2): a queue removes at `Ack`,
  leases, keeps no order past a retry and dead-letters; a stream retains,
  lets each reader own its offset, keeps the order of each partition, and
  leaves a failing record where it is — the reader parks. Neither port can
  grow into the other; that is why this is a domain and not a queue sibling.
- **`Stream` is FROZEN at four methods** (ADR 0039). A capability is a
  sibling found by type assertion; `Waker` is a hint as the queue's is.
- **A batch is one partition's** (ADR 0231 §5): `PARTITIONS_MIXED` rather
  than a split, because a split batch is two syncs and a crash between them
  keeps half. A batch with no key goes to the partitions in turn — the
  engine's state, which is why `PartitionOf` is not asked for it.
- **The reader owns its offset** (§4): `Committed` of a group that never
  committed is `UNKNOWN_GROUP`, never zero; a read below the first retained
  offset is `OFFSET_TRIMMED`, never a silent skip.
- **`PolicyValue` works at its zero** (§6): every zero has one reading, so
  this struct is ADR 0031's clamp branch alone; what it refuses is a negative
  value, a `BlockTimeout` with no `MaxBytes` and a `MaxBytes` below one record.

## Do NOT

- Add a fifth method to `Stream`, or to any sibling: write a sibling.
- Change `PartitionOf`'s hash, or let an engine place keys its own way: every
  stored key would move to another partition and per-key order would break.
- Declare a code under `internal/service/data/stream` (ADR 0160): it goes in
  `design/data/stream.yaml`, in the `0.3.94.*` block here, then `kit gen`.
- Hand-edit `gen_*.go` or a doc comment: the design owns both (ADR 0163, 0167).

## Reference

- ADR 0231 — `docs/adr/0231-a-record-is-kept-until-its-retention-ends-and-every-group-reads-it-at-its-own-pace.md`
- ADR 0054 (the queue), ADR 0039, ADR 0031, ADR 0160, ADR 0230 (seglog)
- Design: `design/data/stream.yaml`; ranges: `design/sdk.yaml` `codes.ranges`
