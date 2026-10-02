# ADR 0153 — the repository carries no devcontainer and no git hooks, and its guards run in CI

- **Status**: Accepted
- **Date**: 2026-10-02
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) (§Decision 5, the `hooks-check` gate; closes the §Deferred item on the four checks only the local hook ran), [ADR 0008](0008-readme-from-code-generation.md) (§Decision 1, `gomarkdoc` shipped by the devcontainer Go feature)
- **Related**: [ADR 0004](0004-sdk-bazel-build-system.md) (`bazel-ci.yml` is the gating lane)

## Context

The repository carried two pieces of developer-machine tooling it did not own.

- **`.devcontainer/`** — 518 files copied from an external devcontainer
  template: the image Dockerfiles, the language features, and the agent
  configuration baked into the image. Nothing in the SDK's build, test or
  release read it. It was rebuilt daily by `docker-images.yml`, a workflow
  inherited with the template, and the agent configuration it shipped now comes
  from versioned plugins installed outside the repository.
- **`.githooks/`** — a `commit-msg` hook refusing AI attribution and a
  `pre-commit` hook that chained the template's checks and then every executable
  `scripts/pre-commit/*.sh`. Both were opt-in: a clone ran them only after
  `scripts/install-hooks.sh`. ADR 0088 measured the commit-msg hook allowing what
  it existed to refuse, gave it a BATS suite and the `hooks-check` gate, and
  recorded under §Deferred that four of the project's checks were enforced by
  nothing but that opt-in hook.

The agent that writes most commits enforces the attribution policy in its own
pre-tool hooks, before `git commit` runs.

## Decision

1. **`.devcontainer/` is removed**, with `docker-images.yml` and every ignore
   rule that named it. `gomarkdoc` is installed with
   `go install github.com/princjef/gomarkdoc/cmd/gomarkdoc@v1.1.0`, the route CI
   already used; the pin stays `v1.1.0`, in the Makefile and in `bazel-ci.yml`.
2. **`.githooks/` is removed**, with `scripts/install-hooks.sh`,
   `scripts/hooks-test.sh`, `scripts/test-commit-msg-hook.bats`, the
   `hooks-check` target and its CI step and `GATES` entry. Commits made by hand
   or by another tool are no longer checked for attribution locally; that is
   accepted.
3. **The guards stay, and CI runs every one of them.** `scripts/pre-commit/`
   keeps its scripts, its BATS suite and `make pre-commit-check`. The three that
   only the hook ran — `check-pkg-docs.sh`, `check-bench-md.sh`,
   `check-error-codes-drift.sh` — become direct steps of `bazel-ci.yml` and
   lines of `make lint`. The fourth, `check-ktn-phases-1-7.sh`, was already
   covered by `make lint-ktn-check`, which runs the same phases where the
   linter's credential exists.
4. **The agent config directory is ignored at every depth** and none of it is
   tracked.

## Consequences / Semantics

- A clone needs no setup step. What blocks a merge is what `bazel-ci.yml` runs.
- The guards that read the tree default to the repository root they sit in,
  instead of a devcontainer mount point.
- `core.hooksPath`, where a clone set it, now points at a directory that does
  not exist; git then runs no hooks.

## Breaking changes

None. No published package changes.

## Alternatives considered

- **Keep `.githooks/` without the devcontainer.** Rejected: the hook ran only on
  clones that opted in, so the policy it carried was never a gate; the guards
  it chained are now gates in CI, and the attribution policy is enforced where
  the commits are actually written.
- **Keep `hooks-check` over a server-side copy of the commit-msg rule.** Not
  done here: a CI check of commit messages is a separate decision, with its own
  cost on every pull request.

## Deferred

- A CI check of commit messages for attribution, should commits made outside
  the agent need it.

## References

- [ADR 0088](0088-a-suite-nothing-runs-is-not-a-test-suite.md) §Decision 5, §Deferred.
- [ADR 0008](0008-readme-from-code-generation.md) §Decision 1.
