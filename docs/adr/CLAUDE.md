<!-- updated: 2026-11-24T12:00:00Z -->
# docs/adr/

## Purpose

Architecture Decision Records. Each ADR captures one cross-cutting decision (layout, error model, build system, code allocation) with rationale, alternatives considered, and impact. ADRs are authoritative — when an ADR and a `CLAUDE.md` disagree, the ADR wins and the CLAUDE.md gets corrected. Treat ADRs as append-only: never edit a merged ADR's decision; if a follow-up changes direction, write a new ADR that supersedes the previous one and update its `Status` field.

## Contents

The ADRs are listed once, in [`INDEX.md`](INDEX.md) — one row per ADR:
file, decision, status — which `scripts/pre-commit/check-domain-docs.sh`
checks against the files on disk (ADR 0251). No `CLAUDE.md` repeats that
list: every agent session loads these files whole, and three copies of it
cost ~400 KB per session. To find the ADR behind a rule, search the ADRs
(a docs search tool where the session has one, else `grep -l` over
`docs/adr/`) or read `INDEX.md`; each ADR file stays authoritative.

## Conventions

- File name: `NNNN-<short-slug>.md` where `NNNN` is the next zero-padded number.
- **Two branches writing ADRs at once: claim distinct numbers, then merge in
  numeric order.** The number is claimed when the file is committed on a branch,
  not when the branch merges, so a second branch reads the first one's claim and
  takes the number after it. Merging low-to-high adds no gap to `main` at any
  point in time, which is what the check below verifies; the five gaps `main`
  already has — 0098–0099, 0105–0109, 0113–0119, 0122–0129 and 0145–0146 — stay, because
  the next ADR takes the number after the highest. Merging high-first
  leaves `main` with a real gap until the other branch lands — legal in the end,
  but the check fails in between, so do not do it. **Renumbering to close that
  window is the one wrong answer**: the branch that already holds the lower
  number would then collide with it, and a number is cited from ADR headers,
  package docs and source comments long before it merges.
- Header block: **Status**, **Date**, **Deciders**, plus **Supersedes** / **Superseded by** / **Amends** / **Amended by** / **Closes** / **Related** where they apply, and since 2026-11 an **Area** (one word of `docs/adr/areas.txt`, the ADR's main family or cross-cutting area) and a **Need** (`decision` a choice between options, `constraint` a rule to follow, `incident` a defect and the rule it produced, `convention` process or tooling, `record` history with no active effect).
- Standard sections, in this order: **Context**, **Decision**, **Consequences / Semantics**, **Breaking changes**, **Alternatives considered** and/or **Why not <option>**, **Deferred**, **References**. This is the same list as `docs/CLAUDE.md` §ADR conventions — keep the two in step; when they disagree, the two are both wrong until they agree.
- A standard section with nothing to say says so rather than being dropped — `Breaking changes` → "None. `<domain>` is a new domain in this change set" is the house form (ADR 0026/0027/0028). A missing heading reads as an oversight; an explicit "None" reads as a decision.
- **The principles are ADR 0154's numbered list.** A domain ADR that departs
  from one cites its number; a principle is added, reworded or withdrawn only by
  an ADR amending 0154, and a withdrawn number is not reused.
- **An incident ADR is folded, then superseded** (ADR 0154 §2). An ADR whose
  subject is a defect found, reproduced and fixed may be written while the fix
  is argued; once its rule is written into the `CLAUDE.md` of the package or
  script it protects, under `## Rules from ADR NNNN` (the rule as a short
  imperative, the lesson in one line), its `Status` becomes `Superseded by
  0154` naming that file, and its text stays unedited as the record. This is
  the same rule as `docs/CLAUDE.md` §ADR conventions.
- **A path in an ADR is the path of its time** (ADR 0155 §Consequences). When
  a package moves, its `CLAUDE.md`, the enforcement artefacts and the index
  (`INDEX.md`) follow it; an ADR's body does not. Only its `Status` and
  `Amended by` lines may be re-pointed — at the current home of a rule an
  incident ADR handed to a package, or at the record that amended it — and
  what changed lands in a new ADR, never in the old one's body.
- Mermaid / ASCII diagrams welcome where they reduce reading time.
- Cross-link with relative paths — from one ADR to another, the bare file name `0005-….md` — so links survive a move.
- Use `1.2.0.3` style for codes in prose, never the hex literal.

## Do NOT

- Edit a merged ADR's `Decision` section. Supersede it via a new ADR that links to the old one and flips the old `Status` to `Superseded by NNNN`.
- Store implementation detail here — keep ADRs about the *decision* and the *constraint*, not the code. Implementation specifics live in the package `CLAUDE.md` / `README.md`.
- Reference internal commit SHAs or PR numbers in the body — the URL in a markdown link is the only authoritative reference.

## Verification

```
ls docs/adr/ | grep -E '^[0-9]{4}-' | sort
# every ADR must follow the NNNN-slug.md pattern; no gap in the numeric sequence
# beyond the five main already has (0098–0099, 0105–0109, 0113–0119, 0122–0129, 0145–0146).

# On a branch, a gap is expected when a lower number is claimed on another
# branch — check before blaming the sequence, and merge low-to-high.
# The fetch is not optional: git ls-remote reads the remote without updating
# origin/*, so without it git ls-tree silently sees nothing for a branch this
# clone has never fetched, and the check reports "no one holds it".
git fetch --prune origin
git for-each-ref --format='%(refname)' refs/remotes/origin \
  | grep -v '^refs/remotes/origin/HEAD$' \
  | while read -r ref; do
      git ls-tree --name-only "$ref" docs/adr/ \
        | grep -oE '[0-9]{4}' | sed "s#^#${ref#refs/remotes/origin/} #"
    done | sort -u -k2
```
