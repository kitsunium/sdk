# docstore (service)

A typed, keyed store of JSON documents with unique and multi-valued secondary
indexes, kept in memory and, given a `core/vfs` filesystem, persisted to it:
one snapshot at rest, one small overlay entry per write — durable before the
write returns, whatever the store holds — folded back into the snapshot as the
overlay grows. Public facade: `pkg/v1/docstore`. ADR 0110. See `CLAUDE.md` and
`BENCH.md`.
