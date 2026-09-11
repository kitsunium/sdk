# ADR 0068 — the layer firewall is a checked graph, because Gazelle's visibility admits the whole repository

- **Status**: Accepted
- **Date**: 2026-09-11
- **Deciders**: SDK maintainers
- **Related**: [ADR 0004](0004-sdk-bazel-build-system.md) (Bazel, and the firewall it put on visibility), [ADR 0001](0001-sdk-go-multimodule-layout.md) (the four layers)
- **Amends**: [ADR 0004](0004-sdk-bazel-build-system.md) §Layer firewall — the *mechanism*, not the direction

## Context

ADR 0004 encodes the dependency direction kernel → core → service → pkg/v1 as
Bazel `package_group` + `visibility`, and states the consequence plainly: "Any
rogue import (e.g. kernel → service) fails `bazel build` at the visibility
check". The root `CLAUDE.md` and `internal/CLAUDE.md` repeat it.

Gazelle does not keep that promise. Its Go extension mirrors Go's own
`internal/` rule: a `go_library` below an `internal` directory is made visible
to the subtree of that directory's parent, and for `//internal/...` the parent
is the repository root — so every such library gets `//:__subpackages__`, a
label that admits the WHOLE repository. The `# gazelle:go_visibility`
directive ADR 0004 relies on only adds labels beside that default; no directive
removes it. At the time of writing 105 of the 193 `BUILD.bazel` files carry the
label, 62 of them already on `main`.

A review of the stack flagged it on two new packages. It was then demonstrated
rather than argued: a throwaway package under `internal/kernel` importing
`internal/core/health` — the upward edge the firewall exists to refuse —
built with `bazel build`, "Build completed successfully". The direction held
only because nobody had yet written such an import. The Go module layout
refuses it under a per-module `GOWORK=off` build, but Bazel reads `go.work`,
and CI runs Bazel.

## Decision

The direction is asserted on the dependency graph itself.
`scripts/check-layer-deps.sh` runs four `bazel query` expressions, and each
must come back empty:

| Layer | May not reach |
|---|---|
| `//internal/kernel/...` | any `go_library` outside the kernel |
| `//internal/core/...` | `//internal/service/...`, `//pkg/...`, `//third-party/...` |
| `//internal/service/...` | `//pkg/...`, `//third-party/...` |
| `//pkg/...` | `//third-party/...` — the public module stays dep-light |

It is wired into `make lint` and into CI as its own step. It is not in the
per-commit hook, whose guards all run without Bazel. It fails CLOSED: a query
that cannot be answered stops it, because a firewall check that passes on an
unreadable graph checks nothing. The throwaway package above makes it fail,
naming `//internal/core/health:health` as the target the kernel reached.

The `package_group` declarations stay: they document the intended consumers and
still bind every target that is not under `internal/`.

## Consequences

- A rogue import now fails `make lint` and CI with the target it reached,
  instead of building. The claim ADR 0004 made becomes true again, by a
  different mechanism.
- The root `CLAUDE.md` and `internal/CLAUDE.md` stop saying that visibility
  enforces the direction, and name the guard instead.
- `third-party/` is now held to its place too: nothing in `pkg/` may reach it,
  which was the reason vendor integrations live in the root module (ADR 0012)
  and had no check behind it.

## Why not

- **Strip `//:__subpackages__` from the BUILD files.** `make build` runs
  Gazelle, which would put it back on every run. Keeping it out takes a
  `# keep` on 105 `visibility` attributes and on every package added later,
  and nothing would notice the one that forgot — the same failure, moved.
- **Move the packages out of `internal/`.** Go's `internal/` rule is what keeps
  consumers out of everything but `pkg/v1`; giving it up to satisfy a build
  tool's visibility heuristic trades the stronger guarantee for the weaker.
- **Rely on the per-module Go builds.** They do refuse the edge, but they are
  not what CI runs, and a guarantee that holds only in the build nobody runs is
  the kind rule 12 exists to name.

## Breaking changes

None. No public symbol, code or target moves; the tree already satisfied all
four queries when the guard was added.

## Deferred

- **Hiding the targets as well as checking them.** Adding `# keep` to every
  `visibility` would make an upward edge fail at `bazel build` again, not only
  at `make lint`. Deferred because the graph check already catches every such
  edge before merge, and the `# keep` discipline would itself need a guard.

## References

- Gazelle Go extension reference — `go_visibility` adds labels beside the
  default internal visibility; `//internal` resolves to `//:__subpackages__`.
- `scripts/check-layer-deps.sh`, `Makefile` (`lint`), `.github/workflows/bazel-ci.yml`.
