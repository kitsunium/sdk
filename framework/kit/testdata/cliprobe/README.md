# cliprobe

Command cliprobe is the smallest product a status line is: one service, one
default fail-safe command that writes a line. The fresh-process benchmark of
`framework/kit` builds and runs it.

A test fixture, not a package anything imports: it is a `main` under
`testdata/`, so docs/api records no symbol of it and tools/genindex writes no
README for it (ADR 0167) — this one is written by hand. See `CLAUDE.md`.
