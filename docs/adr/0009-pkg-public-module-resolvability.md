# ADR 0009 — Public module must be `go get`-resolvable and accessible

**Status**: Accepted; release-layer mechanism implemented. **Amended by ADR 0017** — the module *path* `pkg/v1` is itself unpublishable (Go forbids the `/v1` suffix); the public module is the bare `…/pkg`. First release `pkg/v0.1.0` validated via clean-room `go get`.
**Date**: 2026-05-25
**Deciders**: kitsunium maintainers
**Supersedes**: —
**Amends**: ADR 0007 (release now also cuts `internal/<mod>/vX.Y.Z` resolution tags)
**Related**: ADR 0001 (5-module layout), ADR 0007 (release workflow + versioning)

## Context

ADR 0001 splits the SDK into five Go modules (root + `internal/{kernel,core,service}` + `pkg/v1`) glued by `go.work`, with `replace` directives resolving the intra-repo dependencies. `pkg/v1` is the only consumer-facing module; it imports `internal/*` (legal under Go's `internal/` rule within the same repo path), so `pkg/v1/go.mod` **requires** the three internal modules.

In the working tree those requires use the **zero pseudo-version** and are only satisfied by the local `replace` block:

```gomod
require (
    github.com/kitsunium/sdk/internal/core    v0.0.0-00010101000000-000000000000
    github.com/kitsunium/sdk/internal/kernel  v0.0.0-00010101000000-000000000000
    github.com/kitsunium/sdk/internal/service v0.0.0-00010101000000-000000000000
)
replace ( github.com/kitsunium/sdk/internal/core => ../../internal/core ; … )
```

ADR 0007's `cut-tags.sh` tags the working-tree `go.mod` as-is. A consumer's `go get github.com/kitsunium/sdk/pkg/v1@vX.Y.Z` would therefore fetch a `go.mod` whose internal requires resolve to nothing (the `replace` is ignored for non-main modules; the zero pseudo-version is not a real commit), and `go mod download` fails. This surfaced as a Qodo finding on PR #29.

## Decision

For any module published under `pkg/<major>/`, two properties are **normative**:

1. **Resolvable** — `go get` / `go build` of the published version MUST succeed from a clean machine with **no `go.work`, no `replace`, and no local checkout** of the internal modules. The published module graph is self-resolving from the Go module proxy alone.
2. **Accessible** — the module and its tags MUST be publicly reachable and discoverable: the repository is public, release tags are pushed (so the proxy + `pkg.go.dev` index them), the documented `go get` install line works, and the docs portal links to the live `pkg.go.dev` page. Accessibility is moot until (1) holds — `pkg.go.dev` cannot render a module it cannot resolve.

The `go.work` + `replace` + zero-pseudo setup is retained for **development only**; it MUST NOT leak into a published tag.

## Decision: tag the chain at release (chosen over single-module)

Keep the five-module layout (ADR 0001) and, at release, publish a `go get`-able
form: rewrite every chain module's `go.mod` to drop `replace` and pin its
intra-repo deps to the release version, then tag the whole chain
(`internal/kernel`, `internal/core`, `internal/service`, `pkg/<major>`) at the
same version on a detached release commit. Tags resolve the no-self-reference
problem that blocks pseudo-versions; the dev branch keeps `replace` + `go.work`
untouched. Internal tags are resolution-only — Go's `internal/` rule still
blocks direct consumer import.

The alternative — collapse to one root module — was rejected: it reverses
ADR 0001 and changes the tag scheme (`pkg/<major>/v…` → `v…`), reworking the
docs-versioning + release tooling for no architectural gain.

The release-layer mechanism lives in `scripts/release/` (see its `CLAUDE.md`).

## Consequences

- The **first** release is a deliberate, manually-validated step: it publishes
  the chain's `go.mod`s to the proxy/sumdb permanently, and pushed-tag checksums
  (`go.sum`) cannot be computed before the tags exist. The release tooling
  refuses to auto-cut the first release and a clean-room `go get` must pass
  first; only then is it cut manually. Subsequent releases are automatic.
- Lockstep versioning holds while the chain is at major 0/1. A future `pkg/v2`
  needs the internal modules to adopt `/vN` module paths (the tooling fails
  loud rather than mint an invalid internal tag) — addressed when v2 lands.

## References

- Go modules — pseudo-versions: <https://go.dev/ref/mod#pseudo-versions>
- Go modules — `internal/` visibility within a module path
- ADR 0001 (multi-module layout), ADR 0007 (release workflow)
