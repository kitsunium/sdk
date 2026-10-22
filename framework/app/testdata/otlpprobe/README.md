# otlpprobe

Command otlpprobe is coreprobe with the OTLP export subsystem imported and an
export declared: the same service and command on kit's core, and
`framework/app/otlp`, whose forwarder `framework/app`'s initgraph test finds
linked in it.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
