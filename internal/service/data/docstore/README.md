# docstore (service)

A typed, keyed store of JSON documents with unique and multi-valued secondary
indexes, kept in memory and, given a `core/data/vfs` filesystem, persisted to it:
one snapshot at rest, one small overlay entry per write — durable before the
write returns, whatever the store holds — folded back into the snapshot as the
overlay grows; or over SQL, in a database the caller owns (ADR 0139). Either
keeps, when asked, the last versions of each document in the same write as the
document (ADR 0143). Public facade: `pkg/v1/data/docstore`. ADR 0110. See
`CLAUDE.md` and `BENCH.md`.
