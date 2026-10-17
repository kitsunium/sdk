# coreprobe

Command coreprobe is cliprobe on kit's core alone: the same service and
command, built on `framework/internal/kit` without the facade, which imports
no registration package either since ADR 0179's step 10. It is the control
beside cliprobe of a program that imports no registration package, whose
symbols `framework/kit`'s initgraph test reads.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
