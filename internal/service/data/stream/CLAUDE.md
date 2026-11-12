# internal/service/data/stream/

## Purpose

The home of the engines of `internal/core/data/stream`'s `Stream` port (ADR
0231): memory and the conformance suite every engine runs (#819), a directory
of segments over `kernel/fs/seglog` with retention by age and bytes (#821), a
SQL table joined to the caller's transaction on ADR 0151's pattern (#822), and
the subscriber that reads a stream for a consumer group (#823).

Today it holds its package comment alone (`doc.go`, written by kit from
`design/data/stream.yaml`). It exists before its first engine because ADR 0160
declares the engines' codes, `0.3.94.*`, in the core at THIS path, and
`scripts/pre-commit/check-core-symmetry.sh` requires a layer-3 range's path to
be a service package. #819 fills it.

## Do NOT

- Declare an error code here (ADR 0160): `STREAM_BACKEND_FAILED`,
  `STREAM_DIRECTORY_UNUSABLE`, `SQL_STREAM_MISCONFIGURED` and
  `SUBSCRIBER_MISCONFIGURED` are `core/data/stream`'s; a new one goes in the
  design's `0.3.94.*` block.
- Place a key on a partition any other way than `core/data/stream.PartitionOf`.
- Import `internal/service/app/consensus`: data sits below app (ADR 0155); the
  segmented log is the kernel's (`kernel/fs/seglog`, ADR 0230).
