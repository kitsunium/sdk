# ADR 0251 — an ADR is listed once, and a `CLAUDE.md` stays within its byte budget

- **Status**: Proposed on 2026-11-24 — implemented in the same change (`docs/adr/INDEX.md`, `scripts/pre-commit/check-domain-docs.sh`, the root `CLAUDE.md`, `docs/CLAUDE.md`, `docs/adr/CLAUDE.md`), for the owner to accept or refuse before it merges; revisable while v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md))
- **Date**: 2026-11-24
- **Deciders**: SDK maintainers
- **Area**: docs
- **Need**: decision
- **Amends**: [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) §Consequences ("the three ADR indexes … keep every superseded ADR listed"; "the digest of what each ADR decided stays in `docs/adr/CLAUDE.md`"), and rule 11 of the root `CLAUDE.md` (the three indexes `check-domain-docs.sh` holds to the disk)
- **Related**: [ADR 0008](0008-readme-from-code-generation.md) and [ADR 0138](0138-a-doc-link-resolves-or-it-is-not-written.md) (principle 28: documentation is generated or checked, never trusted), [ADR 0155](0155-every-layer-groups-its-packages-by-family-and-a-path-may-move-while-v0.md) (a path in an ADR is the path of its time; the index follows a moved package)

## Context

A `CLAUDE.md` is not only read by the person who opens it. Every agent
session that works in the repository loads the root `CLAUDE.md` at start, and
the `CLAUDE.md` of every directory it reads into; a subagent loads them again.
They are paid for in full, every time, whatever the task.

Measured on `main` before this change (`wc -c`, 490 files, 4.65 MB in all):
the root `CLAUDE.md` was 140 912 bytes, `docs/CLAUDE.md` 137 261 and
`docs/adr/CLAUDE.md` 210 917, and 74 files were over 12 KB. Of those three,
about 400 KB was the same list written three times — the ADR indexes ADR 0154
kept: `docs/adr/CLAUDE.md`'s Contents table (205 733 bytes),
`docs/CLAUDE.md`'s (130 847) and the root Reference list (65 197). One
session reading a single ADR took in ~275 KB of these files before the ADR
itself.

The indexes earn their place for two readers only: a person browsing the
directory, and `check-domain-docs.sh`, which holds each to the files on disk
since three of them had drifted. Neither needs three copies, and neither needs
them inside a file every session loads. An agent that looks for the ADR behind
a rule searches the ADRs themselves — `grep`, or the docs search and the
decision map its harness builds from `docs/adr/` at session start — and the
ADR file, not a row about it, is authoritative.

## Decision

1. **The ADRs are listed once, in `docs/adr/INDEX.md`.** It is
   `docs/adr/CLAUDE.md`'s former Contents table, moved unchanged — file,
   decision, status — and it is the only index of the ADRs. A new ADR adds
   its row there in the same change; a `Status` change updates its row.
   `check-domain-docs.sh` holds it to the disk exactly as it held the three:
   every numbered ADR named, once, and nothing else.
2. **No `CLAUDE.md` carries an ADR index.** The root Reference list and the
   Contents tables of `docs/CLAUDE.md` and `docs/adr/CLAUDE.md` become a
   pointer to `INDEX.md`. `check-domain-docs.sh` refuses an index row
   (`| \`NNNN-…`, `| \`adr/NNNN-…`, `- ADR NNNN …`) in those three files, so a
   branch cut before this change cannot bring a list back through a union
   merge. A `CLAUDE.md` still cites the ADRs its rules come from; a citation
   is not an index.
3. **A `CLAUDE.md` has a byte budget: 24 KB for the root, 12 KB for any
   other.** It is a target, not a gate: 74 files are over it today, and
   trimming prose by size alone would drop rules somebody needed. A change
   that touches a `CLAUDE.md` over its budget does not grow it, and moves
   out what is a listing rather than guidance — a table that restates the
   code or another document — the way this change moves the indexes. A file
   that is guidance through and through is split along its sections into the
   `CLAUDE.md` of the subdirectories they describe.
4. **ADR 0154 §Consequences is amended** on the two points above: the
   superseded ADRs stay listed, in `INDEX.md`; the digest of what each ADR
   decided is `INDEX.md`, not `docs/adr/CLAUDE.md`. Principle 28 stands
   unchanged — the one index is still checked against the files on disk.

## Consequences / Semantics

- **Implemented by this change**: `docs/adr/INDEX.md`; the three former
  indexes replaced by a pointer; `check-domain-docs.sh` invariant 3 reading
  `INDEX.md` and refusing an index row in the three former hosts, with its
  suite in `scripts/pre-commit/test-pre-commit-guards.bats`; the docs site's
  ADR sync skipping `INDEX.md` as it skips `CLAUDE.md`, since it writes its
  own ADR index page from the files; rule 11, the verification table and the
  docs that described three indexes.
- The root `CLAUDE.md` drops from 140 912 to about 76 000 bytes,
  `docs/CLAUDE.md` from 137 261 to about 7 000 and `docs/adr/CLAUDE.md` from
  210 917 to about 5 700. The root stays over its budget; it and the other
  files over theirs are brought under by their own changes, per §3.
- Writing an ADR touches one list instead of three, and a concurrent branch
  that adds an ADR conflicts in one file instead of three.

## Breaking changes

None. No code, no published shape: documentation and a documentation guard.

## Alternatives considered

### Keep the indexes, gate the budget

A hard gate at 24 KB / 12 KB fails 74 files on the day it lands, and the only
way to pass it is to cut prose by size. The listings are the part that can
move without judgement; the rest needs it.

### No index at all

Every row can be rebuilt from the ADR headers, and the docs site already
builds its own index page that way. But the digest column says in one line
what each ADR decided and what amended it, which the headers do not, and a
person browsing `docs/adr/` reads it there. One checked copy outside the
loaded files costs nothing per session.

### `docs/adr/README.md` instead of `INDEX.md`

GitHub would render it when browsing the directory, but the READMEs of this
repository are written from `docs/api` (ADR 0167); a hand-written one would
read as one of them. `INDEX.md` says what it is.

## Deferred

- Adding `Amended by: ADR 0251` to ADR 0154's header is left to the change
  that next touches ADR headers, so as not to collide with one in flight.
- Bringing each over-budget `CLAUDE.md` under its budget, file by file.

## References

- `scripts/pre-commit/check-domain-docs.sh` — invariant 3
- [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) — §Consequences, principle 28
- `docs/adr/INDEX.md`
