# workflowprobe

Command workflowprobe is coreprobe with the workflow subsystem imported and
two workflows declared over entities and states of distinct types: the same
service and command on kit's core, and `framework/app/workflow` — which
imports `framework/app/store` —, whose engine `framework/app`'s initgraph test
finds linked in it, instantiated once whatever the types.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
