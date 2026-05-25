<!-- updated: 2026-05-18T14:30:00Z -->
# docs/adr/

## Purpose

Architecture Decision Records. Each ADR captures one cross-cutting decision (layout, error model, build system, code allocation) with rationale, alternatives considered, and impact. ADRs are authoritative — when an ADR and a `CLAUDE.md` disagree, the ADR wins and the CLAUDE.md gets corrected. Treat ADRs as append-only: never edit a merged ADR's decision; if a follow-up changes direction, write a new ADR that supersedes the previous one and update its `Status` field.

## Contents

| File | Decision | Status |
|---|---|---|
| `0001-sdk-go-multimodule-layout.md` | Per-sublayer Go modules (`internal/kernel`, `internal/core`, `internal/service`, `pkg/v1`) with `replace` directives + `go.work` umbrella | Accepted |
| `0002-sdk-errors-package.md` | Typed `errs.Define` / `errs.Wrap` infrastructure with `Public` / `Private` split | Accepted (Registry section superseded by 0005) |
| `0003-sdk-codec-package.md` | Universal `Codec` / `StreamingCodec` / `Appender` triad + 10-format registry | Accepted |
| `0004-sdk-bazel-build-system.md` | Bazel 9 with `rules_go` + `gazelle` + `package_group`/`visibility` for layer firewall | Accepted |
| `0005-sdk-error-codes-dotted-quad.md` | `Code uint32` packed as `MM.LL.PP.SS` with wrap trail + canonical `Error()` regex | Accepted |
| `0006-sdk-error-code-registry-extension.md` | Logger v2 + `ring` code-range allocations on top of 0005 | Accepted |
| `0007-sdk-release-and-versioning.md` | Impact-driven patch tags `pkg/<major>/vX.Y.Z` + docs versioning from tag snapshots | Accepted |
| `0008-readme-from-code-generation.md` | `README.md` generated from Go doc comments (gomarkdoc) | Accepted |
| `0009-pkg-public-module-resolvability.md` | Published `pkg/<major>` must be `go get`-resolvable + accessible; release tags the whole module chain | Accepted; mechanism implemented |
| `0010-kernel-recycler-primitive.md` | Generic `recycler.Pool[T]` + `CappedPool[T]` object pools; `buffer`/`scratch` become consumers | Accepted |
| `0011-kernel-snapshot-primitive.md` | Generic `snapshot.Value[T]` copy-on-write container; codec registry consolidates onto it | Accepted |

## Conventions

- File name: `NNNN-<short-slug>.md` where `NNNN` is the next zero-padded number.
- Every ADR has at minimum: **Status**, **Context**, **Decision**, **Consequences**, **Alternatives considered**. Add **Why not <option>** for non-obvious rejects.
- Mermaid / ASCII diagrams welcome where they reduce reading time.
- Cross-link with relative paths: `../adr/0005-…md` so links survive a move.
- Use `1.2.0.3` style for codes in prose, never the hex literal.

## Do NOT

- Edit a merged ADR's `Decision` section. Supersede it via a new ADR that links to the old one and flips the old `Status` to `Superseded by NNNN`.
- Store implementation detail here — keep ADRs about the *decision* and the *constraint*, not the code. Implementation specifics live in the package `CLAUDE.md` / `README.md`.
- Reference internal commit SHAs or PR numbers in the body — the URL in a markdown link is the only authoritative reference.

## Verification

```
ls docs/adr/ | grep -E '^[0-9]{4}-' | sort
# every ADR must follow the NNNN-slug.md pattern; no gaps in the numeric sequence.
```
