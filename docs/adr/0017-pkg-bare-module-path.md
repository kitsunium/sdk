# ADR 0017 — Public module is the bare `pkg`, not `pkg/v1` (Go forbids the `/v1` suffix)

**Status**: Accepted
**Date**: 2026-06-14
**Deciders**: kitsunium maintainers
**Supersedes**: —
**Amends**: ADR 0009 (fixes the module *path*, not just the published `go.mod` form), ADR 0007 (tag shape `pkg/<major>/vX.Y.Z` → bare `pkg/vX.Y.Z` for the v0/v1 module), ADR 0001 (public module path `pkg/v1` → `pkg`)
**Related**: ADR 0004 (Bazel visibility enforces layering, independent of the module boundary)

## Context

ADR 0001 named the consumer-facing module `github.com/kitsunium/sdk/pkg/v1`.
ADR 0009 made the *published `go.mod`* resolvable (drop `replace`, pin the
intra-repo `internal/*` requires to the release version, tag the chain). Both
assumed `pkg/v1` is a normal, publishable Go module path.

It is not. Go's module-path rules forbid a `/v0` or `/v1` major-version suffix:
v0 and v1 are the **suffix-free** major; only `/v2`+ take a suffix. `module.Check`
— the exact validator `go get`, the module proxy, and `go mod` all use — rejects
`github.com/kitsunium/sdk/pkg/v1` at **every** version, including `v1.0.0`:

```
pkg/v1 @ v1.0.0   -> REJECT  (malformed module path: invalid version)
pkg/v1 @ v0.1.0   -> REJECT
pkg    @ v0.1.0   -> LEGAL
pkg/v2 @ v2.0.0   -> LEGAL
pkg/v2 @ v0.1.0   -> REJECT  (major mismatch)
```

The module built locally only because `go.work` + `replace` skip module-path
validation; the proxy never would. So no tag — not even the release tooling's
former hard-coded first `v1.0.0` — would ever be `go get`-able. The ADR 0009
first-release gate (a clean-room `go get` must pass before the first tag is
pushed) is what surfaced this; the tooling would otherwise have cut a tag the
proxy rejects.

## Decision

The public module is the **bare** `github.com/kitsunium/sdk/pkg` (its `go.mod`
moves up one directory, from `pkg/v1/go.mod` to `pkg/go.mod`). The consumer
packages keep living under the `pkg/v1/` directory, so **import paths are
unchanged** — `github.com/kitsunium/sdk/pkg/v1/process`, `…/pkg/v1/codec`, etc.
The major-version-suffix rule constrains *module* paths only, never package
import paths *within* a module, so `v1` as a directory name is legal at any
version. Only the `module` declaration moves; no `.go` source import changes.

Versioning: the bare path carries major 0 or 1. The SDK ships **v0.x.x while
alpha**, hardens to **v1.x.x** at first stable, and a future breaking change
adopts a real `…/pkg/v2` module (`go.mod` at `pkg/v2/`, legal `/v2` suffix, tag
`pkg/v2/v2.0.0`) — which is exactly the old three-component tag shape, now
reserved for that case.

Tag shape becomes `pkg/vX.Y.Z` (X ∈ {0,1}), parallel to the
`internal/<mod>/v[01].Y.Z` resolution tags ADR 0009 already cuts. The ADR 0009
chain mechanism (drop `replace`, pin, tag `internal/{kernel,core,service}` + `pkg`
in lockstep on a detached release commit, atomic push) is otherwise unchanged.

**The first release is `pkg/v0.1.0` (alpha)** — the SDK is not a stable v1 yet.
It was validated by a fully offline clean-room `go get` against a local file
proxy built from the rewritten chain `go.mod`s (the ADR 0009 gate):

```
go get github.com/kitsunium/sdk/pkg/v1/process@v0.1.0   # resolves module …/pkg v0.1.0
go build ./...                                           # + internal/{kernel,core,service} v0.1.0
```

with no `go.work`, no `replace`, and no local checkout.

## Consequences

- Consumers run `go get github.com/kitsunium/sdk/pkg/v1/<pkg>@v0.1.0`; it resolves
  to module `…/pkg`. The module-vs-directory distinction never appears in import
  lines.
- The "frozen public API at v1.0.0" policy (pkg/CLAUDE.md, ADR 0007) now keys off
  the **semver** `v1.0.0` tag, not the `v1/` directory name. During v0.x.x,
  breaking changes remain permitted (alpha).
- Release tooling — `scripts/release/lib/tag-format.sh`, `compute-bumps.sh`,
  `cut-tags.sh` (+ their bats tests) — reworks to a single bare-`pkg` unit:
  `compute-bumps` emits the token `pkg`, `cut-tags` cuts `pkg/vX.Y.Z`. The docs
  site's JS tag-format mirror accepts the bare shape too (ADR 0007 §1 pairing).
- Bazel needs a `pkg/BUILD.bazel` so gazelle's `go_deps` can load `//pkg:go.mod`;
  layering (ADR 0004) is unaffected — it is enforced by `package_group` /
  `visibility`, not by module boundaries.

## Why not …

- **Root module public** (`github.com/kitsunium/sdk`, keep the `/pkg/v1/` import
  paths): would drag the root module's heavy vendor deps (the AWS writers under
  `third-party/`) onto every consumer unless those were first extracted into
  separate modules — a large inversion of ADR 0001's "nothing requires root" +
  the dep-light guarantee. Rejected.
- **`pkg/api` (or another non-`vN` segment)**: legal, but an arbitrary name with
  no idiomatic gain over the bare `pkg`; the `v1/` directory already carries the
  API-version signal inside the import path.

## References

- Go modules — major version suffixes: <https://go.dev/ref/mod#major-version-suffixes>
- Go modules — `module.Check` / path validation
- ADR 0001 (multi-module layout), ADR 0007 (release + versioning), ADR 0009 (resolvability)
