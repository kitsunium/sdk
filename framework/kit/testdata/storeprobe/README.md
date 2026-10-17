# storeprobe

Command storeprobe is coreprobe with the store subsystem imported: the same
service and command on kit's core, and `framework/kit/store`, whose engines
`framework/kit`'s initgraph test finds linked in it.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
