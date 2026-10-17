# missingprobe

Command missingprobe is cliprobe with a store declared and the store
subsystem not imported: a product written against `framework/kit` alone,
which the facade no longer completes (ADR 0179 step 10). Its start is
refused, naming the store and the import to add; `framework/kit`'s initgraph
test builds and runs it.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
