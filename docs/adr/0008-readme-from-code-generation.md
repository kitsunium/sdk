# ADR 0008 — README generation from Go doc comments

**Status**: Accepted
**Date**: 2026-05-22
**Deciders**: kitsunium maintainers
**Amends**: ADR 0004 (Bazel SOT — exempted in §Scope)
**Related**: ADR 0007 (release + docs versioning — sync-versions.mjs unchanged)

## Context

Before this ADR every `pkg/v1/<service>/README.md` and `internal/**/README.md` was hand-authored. The same information existed in three places:

- The package's `// Package X` doc comment (rendered by godoc / pkg.go.dev).
- The hand-authored `README.md` (rendered by GitHub and copied by `docs/site/scripts/sync-versions.mjs` into the docs portal).
- The maintainer `CLAUDE.md` (rationale, layering, "do not" notes).

The three drifted constantly. Bug-fixes landed in one place; consumers reading any other view saw stale prose. The pre-commit guard `check-pkg-docs.sh` enforced *existence* (README + CLAUDE.md for every `pkg/v1/**`) but had no signal for *content* drift.

## Decision

Adopt the Go-community-standard tool [`gomarkdoc`](https://github.com/princjef/gomarkdoc) as the single source of truth: every consumer-facing `pkg/v1/<service>/README.md` is generated from its package's Go doc comment.

Mechanics:

1. **Pin via the Go 1.24+ `tool` directive** in `pkg/v1/go.mod`:
   `tool github.com/princjef/gomarkdoc/cmd/gomarkdoc` (pinned to `@v1.1.0`). Invocation: `go tool gomarkdoc` (no proxy fetch at run time).
2. **`//go:generate` directives** sit in the existing primary file of each package (`pkg/v1/codec/codec.go`, `pkg/v1/errs/accessors.go`, `pkg/v1/logger/logger.go`). No new `doc.go` files are created (user feedback memory: no empty stub files).
3. **`make docs-readme`** regenerates all three READMEs via a single `go generate ./codec ./errs ./logger` from `pkg/v1`. A Go-version guard rejects toolchains older than 1.24.
4. **Drift gate**: `scripts/pre-commit/check-readme-drift.sh` calls `go tool gomarkdoc --check` over the three packages. Wired into `make lint`, the pre-commit hook chain, and `.github/workflows/bazel-ci.yml` (between the gazelle drift check and `bazel build`, so a stale README fails fast).
5. **Determinism gate**: `scripts/pre-commit/check-readme-determinism.sh` runs `gomarkdoc` twice into separate temp dirs and `diff -r` must be empty. Catches non-determinism introduced by future gomarkdoc upgrades.
6. **govulncheck** runs on the pinned `gomarkdoc` binary as part of the same CI lane.

The canonical [Package X] doc comment carries headings (`// # Surface`, `// # Quick start`, `// # Activation`, `// # Errors`) using the Go 1.19+ syntax; gomarkdoc renders them as Markdown H2/H3.

### Audience split

Three audiences, three surfaces:

- **Consumers** → package doc comment, rendered as `README.md` and as the pkg.go.dev page.
- **Maintainers** → `pkg/v1/<service>/CLAUDE.md` (additive — Why-this-shape, layering rationale, error-range allocations, `Do NOT` lists).
- **Project-level decisions** → `docs/adr/*.md`.

`internal/**/README.md` files are removed; the existing `internal/**/CLAUDE.md` files already serve the maintainer audience and `check-pkg-docs.sh` accepts CLAUDE.md alone for `internal/*`.

## Scope

README generation lives outside the Bazel build graph by design, mirroring ADR 0007 §Scope for release orchestration. ADR 0004 makes Bazel the source of truth for *build* artefacts; documentation rendering is a sibling concern that reads from `go/doc` (a Go stdlib package) and writes Markdown — no compilation, no caching benefit from Bazel hermeticity, and a `genrule` wrap would obscure the iteration loop (`make docs-readme` is the fast path).

## Consequences

- **Positive**: drift becomes impossible. CI blocks any PR that edits prose in one surface without regenerating the others. Code review of doc changes happens in the same diff as the API change. pkg.go.dev and the generated README share content byte-for-byte.
- **Positive**: maintainer rationale and consumer prose are no longer co-mingled in the same file; the `CLAUDE.md` files keep "Why" and the doc comments keep "What" + "How".
- **Negative — known trade-off**: the Go 1.24 `tool` directive registers gomarkdoc's transitive `require` entries in `pkg/v1/go.mod`'s **require block** alongside production deps. They are visible in `go list -m all` and end up in `go mod vendor` output for any consumer of `pkg/v1`. They never compile into the published API (no production code imports them) and `go mod tidy` keeps them flagged `// indirect`. The alternative — isolating gomarkdoc in a separate `tools/` module — was rejected because it would have added a 6th Go module to the workspace (the SDK already has five glued by `go.work`) and complicated `MODULE.bazel`. Accepting metadata-graph bloat over module-count bloat is the conscious call.
- **Negative**: `gomarkdoc`'s default template structure (`Index / Constants / Variables / Functions / Types`) loses the bespoke section ordering of the hand-authored READMEs. The prose now flows from the package comment + per-identifier comments; Phase A.5 (custom `.gotxt` templates under `pkg/v1/.gomarkdoc/templates/`) is the documented fallback if the default style is rejected.
- **Negative**: `gomarkdoc`'s upstream is a single-maintainer project. `govulncheck -mode=binary` runs in CI and the pinned version lives in `go.sum`. If upstream goes unmaintained the fallback is a 200-line hand-rolled generator built on `go/parser` + `go/doc` + `text/template` (blueprint preserved in `.claude/contexts/readme-from-code-generation.md` §5).

## Alternatives considered

- **`godocdown`** (rejected) — unmaintained since 2013, no `--check` mode, no template customisation.
- **Separate `tools/` Go module** (rejected) — 6th module in the workspace, MODULE.bazel + go.work churn, exhausted at v2's first refine.
- **`gen-crd-api-reference-docs`** (rejected) — CRD-shaped; assumes Kubernetes-style `+marker` comments; over-fitted for our case.
- **Bazel `genrule` around gomarkdoc** (rejected for now) — adds a layer of indirection without observable benefit; the `go tool` invocation is fast enough to keep `make docs-readme` interactive.
- **Hand-rolled `go/doc` + `text/template` generator** (deferred fallback) — only meaningful if the gomarkdoc default style is unacceptable AND custom templates also fall short.
- **Keep hand-authored READMEs** (rejected — this is the drift this ADR fixes).

## Why-not blanket external doc tools

Sphinx, rustdoc, doxygen, jsdoc are all viable in their language ecosystems but each requires a non-Go toolchain step. The SDK is Go-only; staying on the Go stdlib + a single Go binary keeps the dependency graph tight and avoids cross-toolchain orchestration in CI.

## Deferred

The following land under their own follow-ups; out-of-scope for this ADR:

- Custom `gomarkdoc` `.gotxt` templates if the default style is rejected after a maintainer review pass.
- `Example_xxx` functions in `_test.go` files (gomarkdoc renders them automatically under each referent — quality-of-life addition).
- Strict `revive` lint rules (`exported`, `package-comments`) wired into `make lint`.
- Cross-machine determinism (Linux vs macOS, Go patch variance) — pin toolchain via the `toolchain` directive in `pkg/v1/go.mod` if it ever appears.
- Sub-package scope sync: when a new `pkg/v1/<sub>` lands, update the `go generate ./codec ./errs ./logger` arg list AND the drift script in a single PR.
- A pre-commit hook enforcing `// # Activation` blank-import documentation when a package adds new `_ "import"` lines.

## References

- [go.dev/doc/go1.24 — tool directive](https://go.dev/doc/go1.24#tool-directive)
- [go.dev/doc/comment — Go Doc Comments syntax](https://go.dev/doc/comment)
- [pkg.go.dev/github.com/princjef/gomarkdoc](https://pkg.go.dev/github.com/princjef/gomarkdoc)
- [kubernetes.io — Generate Kubernetes Reference Docs](https://kubernetes.io/docs/contribute/generate-ref-docs/kubernetes-api/) (pattern precedent)
- [Cobra doc generation](https://github.com/spf13/cobra/blob/main/site/content/docgen/md.md) (CLI-side precedent for the same idea)
- ADR 0001 — multi-module layout (5-module invariant preserved)
- ADR 0004 — Bazel as build SOT (exempted by §Scope)
- ADR 0005 — error codes (no changes; pkg/v1/codec range 1.2.0.* still rendered in the codec README)
- ADR 0007 — release + docs versioning (sync-versions.mjs continues to copy the regenerated READMEs unchanged)
