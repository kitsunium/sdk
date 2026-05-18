<!-- updated: 2026-05-18T14:30:00Z -->
# .github/workflows/

## Purpose

CI/CD automation. The SDK lane is `bazel-ci.yml`; the other three workflows are inherited from the devcontainer-template repo and path-gated on `.devcontainer/**`.

## Workflows

| File | Trigger | Description |
|---|---|---|
| `bazel-ci.yml` | push to `main`, PRs | Primary SDK CI — drift check + build + test + coverage via Bazel 9 |
| `docker-images.yml` | weekly + push to `.devcontainer/images/**` | Template-inherited; two-tier base+main image build |
| `publish-features.yml` | push to `.devcontainer/features/**` | Template-inherited; publishes OCI feature artifacts |
| `release.yml` | push to main on `.devcontainer/**` | Template-inherited; builds `claude-assets.tar.gz` and tags `vYYYY.MM.DD-<sha7>` |

## bazel-ci.yml (the SDK lane)

Single job `bazel` on `ubuntu-latest`, timeout 120 min. Steps in order:

1. `bazel-contrib/setup-bazel@…` — caches `bazelisk`, disk cache keyed on `.bazelrc`+`.bazelversion`+`MODULE.bazel`+all `go.mod`/`go.sum`, plus the repository cache.
2. **Drift check** — `bazel mod tidy && bazel run //:gazelle`, then `git diff --exit-code` AND a check for untracked `BUILD.bazel`/`go.mod`/`go.sum`. Catches both modifications and new files (post-audit finding #11).
3. `bazel build --config=ci //...`
4. `bazel test --config=ci //...`
5. `bazel coverage --combined_report=lcov //...` → uploaded as `coverage-${{ github.run_number }}` artifact (per-run unique name so concurrent runs don't dedupe, post-audit finding #28).

Concurrency: `${{ github.workflow }}-${{ github.ref }}` with cancel-in-progress.

## Conventions

- Action references SHA-pinned with a trailing `# vX` comment (e.g. `actions/checkout@93cb6efe…  # v5`).
- `permissions: contents: read` only — nothing in the SDK lane writes back.
- `bazel-ci.yml` is the only source-of-truth gate. CI does NOT run `go test`, `golangci-lint`, or `govulncheck` directly anymore (ADR 0004).

## Do NOT

- Re-introduce a `go test` matrix here — drift between local Bazel and CI defeats ADR 0004's "single build system" decision.
- Drop the drift check; gazelle-generated `BUILD.bazel` files must be committed.
- Edit the template-inherited workflows here for SDK reasons.
