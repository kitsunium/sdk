# ADR 0253 — a `CLAUDE.md` holds what to obey, and its reference moves to `NOTES.md`

- **Status**: Proposed on 2026-11-25 — implemented in the same change (the root `CLAUDE.md` and `NOTES.md`, `framework/internal/app/CLAUDE.md` and `NOTES.md`, `scripts/pre-commit/check-domain-docs.sh`, `docs/site/scripts/sync-versions.mjs`), for the owner to accept or refuse before it merges; revisable while v0 ([ADR 0040](0040-changing-a-published-shape-while-v0.md))
- **Date**: 2026-11-25
- **Deciders**: SDK maintainers
- **Area**: docs
- **Need**: decision
- **Amends**: [ADR 0251](0251-an-adr-is-listed-once-and-a-claude-md-stays-within-its-byte-budget.md) §Decision 3 (the 24 KB / 12 KB byte budgets)
- **Related**: [ADR 0167](0167-a-doc-comment-is-the-designs-and-a-readme-is-written-from-docs-api.md) (a README is written from `docs/api`), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (an incident's rule lives in the `CLAUDE.md` of its code)

## Context

A `CLAUDE.md` is the agent instructions of its directory. The agent
instructions this repository is worked with load the root one whole at every
session start, and load it again after the conversation is compacted; a
subdirectory's loads when an agent first reads a file under it. Their
documentation sets two numbers: target under 200 lines per file, since a
longer one costs context and is followed less reliably; and a subdirectory
file loaded on demand is cut past 40 000 characters on a 200 000-token
window (150 000 to 200 000 on a 1M one) — the agent then sees its start only.

ADR 0251 gave each file a byte budget instead — 24 KB for the root, 12 KB for
any other — as a target. Measured on `main` before this change: the root
`CLAUDE.md` is 522 lines and 76 029 bytes, `framework/internal/app/CLAUDE.md`
1 024 lines and 182 567 bytes — cut in every case, so its error codes, its
tests and most of its invariants were never seen by an agent that loaded it —
and 55 files are over 200 lines. The bytes measure what a session pays; the
truncation is what makes an oversized file wrong rather than costly, and the
line count is what the instructions' own guidance is written in.

Most of the length is not instruction. The root carries the whole package
tree of `internal/service/`, `pkg/`, `third-party/` and `framework/` (about
300 lines) and a map of the repository; the framework's implementation
carries a file-by-file table (115 000 bytes in 110 lines), the mechanism and
proof of each of its 49 invariants, its error-code table and how its test
suite is built. All of it is worth keeping, and none of it needs to be read
before every task in the directory.

## Decision

1. **A `CLAUDE.md` targets 200 lines, and one below the root stays under
   40 000 characters.** These replace ADR 0251's byte budgets. The line count
   is a target, as the byte budget was; the character ceiling is the point
   past which the file is cut, so a file over it is a defect to fix in the
   next change that touches it. A change to a file over either does not grow
   it.
2. **A `CLAUDE.md` keeps what an agent must obey or know before it edits the
   directory**: the rules, the invariants, the pitfalls, the commands, and the
   sections a guard reads (the root's Purpose table and `core/` block,
   `check-domain-docs.sh`). An invariant whose full text does not fit keeps
   its statement in one line there.
3. **The rest moves, unchanged, to `NOTES.md` in the same directory** —
   history, rationale, long reference tables, file maps, worked examples, the
   full text of an invariant. It is moved, not rewritten and not dropped:
   every line the `CLAUDE.md` loses is in its `NOTES.md`, and a section keeps
   its title there, so a reference to "`CLAUDE.md` §Test" is found by its
   title one file over. The `CLAUDE.md` names its `NOTES.md` and the
   sections it holds, as a code path rather than a link: the doc-link rule of
   the linter (KTN-DOC-MDREF) lets a `CLAUDE.md` link only to `README.md`,
   `CLAUDE.md` and `COVERAGE.MD`.
4. **`check-domain-docs.sh` reports the files over either size**, their
   number and the five largest, and still exits 0. The other files over are
   brought under one change at a time, as ADR 0251 §3 already planned; a gate
   comes when none is left.
5. **The docs site publishes a root section whole**: a section the root
   `NOTES.md` holds under the title of a published `CLAUDE.md` section is
   appended to it (`sync-versions.mjs`), so the Concepts page still shows the
   whole architecture tree.

## Consequences / Semantics

- **Implemented by this change**: the root `CLAUDE.md` from 522 lines /
  76 029 bytes to 192 / about 50 600, its tree past `core/`, its `baseenc`
  note and its Layout map in `NOTES.md`; `framework/internal/app/CLAUDE.md`
  from 1 024 / 182 567 to 200 / about 12 800, its Files table, its invariants
  in full, its error codes and its Test section in `NOTES.md`, each invariant
  kept as one line; the size report in `check-domain-docs.sh` with its suite
  in `test-pre-commit-guards.bats`; the docs site appending a `NOTES.md`
  section.
- An agent working in `framework/internal/app` now sees every rule and every
  invariant of the directory, where it saw the first 40 000 characters before.
  It reads `NOTES.md` for the mechanics of what it changes — one more read,
  paid only by the task that needs it.
- A search of the agent instructions that indexes `docs/**/*.md` and every
  `CLAUDE.md` by name does not see a `NOTES.md` until it adds that name;
  `grep` does.
- 53 files remain over 200 lines and 11 below the root over 40 000 bytes; the
  guard names the largest at every run.

## Breaking changes

None. No code, no published shape: documentation, a documentation guard's
report and the docs site's assembly of a page.

## Alternatives considered

### `README.md` as the sibling

The name every reader opens first, and a link KTN-DOC-MDREF allows. But the
READMEs of `pkg/` and `framework/` are written from `docs/api` by
`make docs-readme` (ADR 0167, root rule 10): a hand-written part would be
overwritten, and a hand-written README elsewhere reads as one of them.

### A tree under `docs/` mirroring the directories

`docs/**` is indexed by name everywhere already. But a note away from its code
is the one that does not travel with it (root rule 11): a package that moves
leaves its notes behind, and the reader of a directory does not find them
beside its `CLAUDE.md`.

### Split along subdirectories only

ADR 0251 §3's answer for a file that is guidance through and through. The
two files here are not: the root's tree and map describe the repository
rather than a subdirectory, and `framework/internal/app` is one package —
its file map has no subdirectory to go to.

### Cut the prose to fit

Trimming by size drops what somebody needed; the moved-not-lost rule is what
makes the split reviewable — a line count of the diff, not a judgement of
every sentence.

## Deferred

- The 53 other files over 200 lines, file by file, then a gate.
- Adding `NOTES.md` to the agent instructions' document search, outside this
  repository.

## References

- `scripts/pre-commit/check-domain-docs.sh` — part 4, the size report
- `docs/site/scripts/sync-versions.mjs` — the root sections and `NOTES.md`
- [ADR 0251](0251-an-adr-is-listed-once-and-a-claude-md-stays-within-its-byte-budget.md) — §Decision 3, amended
- Memory documentation of the agent instructions this repository is worked
  with: "target under 200 lines per CLAUDE.md file", and the 40 000-character
  cut of a file loaded on demand
