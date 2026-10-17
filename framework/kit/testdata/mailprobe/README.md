# mailprobe

Command mailprobe is coreprobe with the mail subsystem imported and a mailer
declared: the same service and command on kit's core, and `framework/kit/mail`
— which imports `framework/kit/queue` and `framework/kit/store` —, whose
outbox and transports `framework/kit`'s initgraph test finds linked in it.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
