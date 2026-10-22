# traceprobe

Command traceprobe is a whole product on `framework/app`, every subsystem but
the Studio imported: a store holding a personal field, a frontend served from
embedded files, a topic and its subscription, a workflow, a mailer, two jobs,
a command and a query exposed over HTTP, a webhook, a setting and a secret,
and an OTLP export, off until its endpoint is set. `framework/app`'s guard builds it as a release is built and reads its sources,
its binary and what it says when it runs.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
