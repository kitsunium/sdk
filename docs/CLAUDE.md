<!-- updated: 2026-11-24T12:00:00Z -->
# docs/

## Purpose

Long-form SDK documentation. ADRs here are the source of truth for cross-cutting decisions; package-level READMEs are the short-form mirror; `api/` is the exported API itself, written from the code.

## Contents

The ADRs are listed once, in `adr/INDEX.md` — one row per ADR: file,
decision, status (ADR 0251). `scripts/pre-commit/check-domain-docs.sh` checks
only that every ADR number on disk appears there exactly once, and no other;
the Decision and Status columns are kept up to date by hand. No `CLAUDE.md` repeats that
list: every agent session loads these files whole, and three copies of it
cost ~400 KB per session. To find the ADR behind a rule, search the ADRs
(a docs search tool where the session has one, else `grep -l` over
`docs/adr/`) or read `INDEX.md`; each ADR file stays authoritative.

The rest of `docs/` is listed under §Subtree.

## ADR conventions

- Sequential numbering (`0001`, `0002`, …). Never reuse a number.
- Filename slug = short kebab-case summary of the decision.
- Header fields: `Status`, `Date`, `Deciders`, optional `Supersedes`, `Superseded by`, `Amends`, `Amended by`, `Closes`, `Related`.
- Standard sections, in this order: Context, Decision, Consequences / Semantics, Breaking changes, Alternatives considered and/or Why not …, Deferred, References. This is the same list as `docs/adr/CLAUDE.md` §Conventions — keep the two in step. Both forms are in use: ADRs 0093 to 0138, 0141 and 0142, and many older ones, have an `## Alternatives considered` section, some with `### Why not …` headings under it; ADRs 0139 and 0140, and many older ones, have a `## Why not …` section. This line named "Why not …" alone after that file listed both, and an automated review read the omission as a rule and flagged the heading.
- Immutable after merge. Supersede via a new ADR that references the old one (e.g. ADR 0005 supersedes ADR 0002 §Registry; ADR 0006 amends ADR 0005 §Registry).
- The cross-cutting principles are ADR 0154's numbered list (the charter); a principle changes only by an ADR amending 0154.
- A path in an ADR is the path of its time (ADR 0155): a moved package takes its `CLAUDE.md` and the enforcement artefacts with it, never an ADR's body, and the rows of `adr/INDEX.md` that name its path are updated where they stand (the index stays at `docs/adr/INDEX.md`); only an older ADR's `Status` and `Amended by` lines are re-pointed. Same rule as `docs/adr/CLAUDE.md` §Conventions.
- An incident ADR — a defect found, reproduced and fixed — is folded, then superseded (ADR 0154 §2): its rule goes into the `CLAUDE.md` of the package or script it protects, under `## Rules from ADR NNNN`, its `Status` becomes `Superseded by 0154` naming that file, and its text stays as the record. Same rule as `docs/adr/CLAUDE.md` §Conventions.

## Registry coupling

The dotted-quad allocation table in `adr/0005-…` + the extension in `adr/0006-…` is the **documentary source of truth**; the `codeRangeOwners` table in `internal/kernel/errs/gen_ranges_test.go`, written by kit from `design/sdk.yaml`'s `codes.ranges` (ADR 0164), is the **executable source of truth** (ADR 0035). Keep the two in sync manually on every change — the ownership audit fails on a range absent from that table, and `registry_external_test.go` catches drift on uniqueness and `reason = screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")` (ADR 0006/0020) over every package in `//:audit_sources`.

`docs/error-codes.yaml` is the **generated human-readable list** of every error code a package of the workspace declares (one entry per code: dotted-quad, const name, hex, package). It is written from `api/` — the code's API, read with go/types and held to the code by `make api-check` — by `make error-codes` (`scripts/gen-error-codes.sh`, `tools/genindex -write-error-codes`), which `make api` runs after writing `api/`: every `errs.Code` constant whose name starts with `Code` and that re-exports no other constant, whatever spelling declares it — a hex literal, an `iota` row —, the errs package's six meta-codes `0.0.0.1`–`0.0.0.6` included and its four masks, of the code type and no code, left out. The `scripts/pre-commit/check-error-codes-drift.sh` guard (`tools/genindex -check-error-codes`) fails `make lint` and CI when the file is not what `api/` writes, naming each entry. It replaced a grep over the sources that listed what a regular expression matched: two codes a doc comment's example wrote, which no package declares, and none of the six meta-codes, which `iota` declares. It is a convenience index, not authoritative — the AST audit remains the executable gate; tools/genindex's `Test_errorCodesCensus` holds the file to the sources read with go/parser alone, and `Test_apiCodes` to `api/`.

## Do NOT

- Put feature documentation here. Packages document themselves via `README.md` next to their code.
- Delete an accepted ADR. Mark it superseded by adding a new ADR and updating both `Status` lines.
- Re-number existing ADRs, even after a supersede chain.
- Edit an ADR's Decision section after merge — write a new ADR.

## Subtree

- `adr/` — Architecture Decision Records, listed in `adr/INDEX.md` (ADR 0251; the index is the count, so no separate number goes stale here)
- `site/` — the documentation website (Astro) — see `site/CLAUDE.md`
- `error-codes.yaml` — every error code a package declares, written from `api/` by `make error-codes` (see §Registry coupling)
- `api/` — the exported API of every module of `go.work`, one `<module>.json` each (`sdk.json`, `sdk/third-party/aws.json`, …): every exported symbol, internal packages included, with its go: id, kind, signature as its file spells it and canonically, owner, doc, cells, file, codes, layer and family, read from the code with go/types on the twelve cells of `scripts/ci/platforms.sh`. Written by `make api` (`tools/genindex -write-api`) and checked byte for byte by `make api-check`, in `make lint-check`, which also holds the code's surface to the markers of the pins kit writes from `design/` and every generated file to its design file's bytes (ADR 0163): a doc edit is followed by `make api`, nothing else. `api/schema.json` is the format, with the id and canonical-signature vectors kit's side tests too. The docs portal renders it — the ⌘K index and the API section of every package page, an alias's members included (`site/CLAUDE.md`). Never edited by hand
- `getting-started.md` — the consumer quickstart (logger, codec, errs)
- `BENCHMARK-TEMPLATE.md` — the shape every kernel `*_bench_test.go` follows
- `PROFILING.md` — driving the pprof + benchstat pipeline over the codec bench matrix
- `alloc-hot-paths.md` — the ten per-record allocation rows of #813, measured on one base with the cause of each; their fixes belong to the packages' own changes (ADR 0216 §Deferred)
- `pre-v1-published-shape-audit.md` — every concrete shape `pkg/v1` publishes by alias, regenerated from the tree (read by `pkg/v1/CLAUDE.md`)
- `sdk-feature-ideation.md` — feature ideation across the Go ecosystem, the SDK and the framework (cited by ADR 0013)
