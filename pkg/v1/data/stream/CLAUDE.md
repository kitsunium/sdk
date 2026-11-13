<!-- updated: 2026-11-12T00:00:00Z -->
# pkg/v1/data/stream/

## Purpose

The public facade for the SDK's durable, partitioned, offset-addressed log
(ADR 0231, milestone 8 of kitsunium/sdk#890): type aliases onto
`internal/core/data/stream`, the three engine constructors — memory, file over
`kernel/fs/seglog`, SQL joined to the caller's transaction —, the SQL engine's
migration, `PartitionOf`, and the subscriber `Subscribe`.

## Surface

| Kind | Names |
|---|---|
| Port | `Stream` (frozen at four methods: `Append`, `Read`, `Bounds`, `Partitions`) |
| ADR 0039 siblings | `Waker`, `Seeker`, `Trimmer`, `Compactor`, `Committer` — every engine here implements all five, reached by type assertion; the file engine also answers `io.Closer` |
| Values | `Offset`, `Record` (= `RecordValue`), `Entry` (= `EntryValue`), `Appended` (= `AppendValue`), `Bounds` (= `BoundsValue`), `Policy` (= `PolicyValue`), `Compacted` (= `CompactValue`) |
| Configs | `FileConfig`, `SQLConfig`, `MemoryConfig`, `SubscriberConfig`, `ParkConfig`; `Handler` (func port), `CommitMode` |
| Constructors | `NewMemory`, `NewFile`, `NewSQL` |
| Functions | `SQLMigration` (the three tables, a `sql.Migration` the caller numbers), `PartitionOf` (FNV-1a modulo the partitions) |
| Engine | `Subscribe` |
| Constants | `DefaultMaxRecordBytes`, `DefaultSegmentBytes`, `CommitAfterHandler`, `CommitByHandler`, and one `Code…` per sentinel |
| Sentinels | `StreamMisconfigured`, `RecordTooLarge`, `OffsetTrimmed`, `OffsetAhead`, `UnknownPartition`, `InvalidBatchSize`, `StreamFull`, `UnknownGroup`, `PartitionsMixed`, `StreamBackendFailed`, `StreamDirectoryUnusable`, `SQLStreamMisconfigured`, `SubscriberMisconfigured` |

## Why this shape

- **Everything is an alias or a forwarder, written by kit.** `gen_facade.go` is
  the `facade:` of the third package in `design/data/stream.yaml` (ADR 0166);
  `doc.go` is that package's `doc:` (ADR 0167). Edit the design, run
  `kit gen`, then `make api` and `make docs-readme`; never edit either file.
- **The value aliases drop the `Value` suffix, as the queue's do**: `Record`,
  `Entry`, `Bounds`, `Policy`; the two answers are past participles,
  `Appended` and `Compacted`, as the queue's `Purged`.
- **The package doc leads with the frontier table queue/stream.** The two
  packages carry bytes between processes durably and keep them for opposite
  reasons — removal at acknowledgement against retention, competing consumers
  against groups that each read everything — and a reader who picks the wrong
  one finds out in production.
- **Three worked examples render in `README.md`** from
  `stream_external_test.go` (genindex attaches an `Example<F>` to `F`):
  `ExampleNewMemory` (a keyed append read back from its partition),
  `ExampleSubscribe` (a group subscriber, its offset committed) — both run
  with their output checked — and `ExampleNewSQL` (an Append inside the
  caller's transaction and a handler committing under `CommitByHandler`),
  compiled only: it needs a database.
- **Every name is built by a test with no internal import**
  (`stream_external_test.go`): the five siblings on the three engines, the
  first refusals by sentinel and by code, `SQLMigration`.
- `sql` is the one sibling import, as for `queue` and `docstore`:
  `SQLMigration` and `SQLConfig.Dialect` speak `sql.Dialect` and
  `sql.Migration`, never an internal package.

## Platforms

`NewFile` refuses with `UNSUPPORTED_PLATFORM` on Windows, illumos and Solaris:
the groups' cursors live in `vfs.NewOS`'s disk filesystem, which those
platforms do not have (ADR 0018). The memory and SQL engines run everywhere.
`requireFileEngine` in the external test asserts that refusal there, then
skips (ADR 0095).

## Do NOT

- Add a second spelling of a verb (`Publish` for `Append`, `Consume` for
  `Subscribe`): the frontier with `queue` is what the names carry.
- Re-export an engine's concrete type: the engines stay unexported behind
  their constructors; the configs are aliases onto configuration structs.
- Hand-edit `README.md`, `doc.go` or `gen_facade.go`.

## Reference

- ADR 0231 — `docs/adr/0231-a-record-is-kept-until-its-retention-ends-and-every-group-reads-it-at-its-own-pace.md`
- ADR 0166, ADR 0167 — the facade and its doc are the design's
- `internal/core/data/stream/CLAUDE.md` — the port, its siblings and its policy
- `internal/service/data/stream/CLAUDE.md` and `BENCH.md` — the engines and what they cost
