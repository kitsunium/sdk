# workflowprobe

Command workflowprobe is coreprobe with the workflow subsystem imported: the
same service and command on kit's core, and `framework/kit/workflow` — which
imports `framework/kit/store` —, whose engine `framework/kit`'s initgraph test
finds linked in it, instantiated once.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
