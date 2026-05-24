# ADR 0009 — Public module must be `go get`-resolvable and accessible

**Status**: Accepted (requirement); implementation Deferred
**Date**: 2026-05-25
**Deciders**: kitsunium maintainers
**Supersedes**: —
**Amends**: —
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

## Implementation directions (Deferred — pick in a follow-up `/plan`)

- **(Recommended) Rewrite at tag time.** Before tagging, `go mod edit` each `pkg/<major>/go.mod` to require `internal/*@<real pseudo-version of the release commit>` and drop the `replace` block, then tag *that* tree. The internal modules resolve from the public repo via commit pseudo-versions (the commit is pushed at release). Least disruption to ADR 0001; lands in the release tooling ADR 0007 already owns.
- **(Alternative) Single module.** Collapse to one `go.mod` at the root; Go's `internal/` rule still hides `internal/*` from consumers. Simplest resolution, but reverses ADR 0001's multi-module decision.

`verify_module_graph` in `cut-tags.sh` (which strips replaces and runs `go mod download`) is the gate that proves property (1); it cannot pass until the chosen direction is implemented.

## Consequences

- The first real release tag MUST NOT be cut until property (1) is implemented and `verify_module_graph` passes from a clean environment — otherwise the published `pkg/v1` is broken for every consumer.
- PR #29's `5e861e3` fixed the *mechanical* half of the gate (block-aware `replace` strip, the leaked `RETURN` trap, the `modroot` bug) so the verifier is correct; this ADR records the *publishability* requirement the verifier must ultimately satisfy.

## References

- Go modules — pseudo-versions: <https://go.dev/ref/mod#pseudo-versions>
- Go modules — `internal/` visibility within a module path
- ADR 0001 (multi-module layout), ADR 0007 (release workflow)
