<!-- updated: 2026-05-18T14:30:00Z -->
# .github/workflows/

## Purpose

CI/CD automation. The SDK lanes are `bazel-ci.yml` (gate) and `sdk-release.yml` (auto-tag after the gate); the remaining three workflows are inherited from the devcontainer-template repo and path-gated on `.devcontainer/**`.

## Workflows

| File | Trigger | Description |
|---|---|---|
| `bazel-ci.yml` | push to `main`, PRs | Primary SDK CI — drift check + build + test + coverage via Bazel 9 |
| `sdk-release.yml` | `workflow_run` after `SDK CI (Bazel)` success on `main`, plus manual `workflow_dispatch` | Impact-driven patch tags `pkg/<major>/vX.Y.Z` (see ADR 0007). Reads majors from `scripts/release/compute-bumps.sh` and pushes via `scripts/release/cut-tags.sh`. First release is held unless dispatched manually (`--allow-bootstrap`, ADR 0009). |
| `docs-deploy.yml` | `workflow_run` after `SDK Release`, push to `main` on docs paths, manual `workflow_dispatch` | Build + deploy the versioned docs portal (`docs/site`) to GitHub Pages. Separate from release (deploy is a consequence, not a release step). |
| `docker-images.yml` | weekly + push to `.devcontainer/images/**` | Template-inherited; two-tier base+main image build |
| `publish-features.yml` | push to `.devcontainer/features/**` | Template-inherited; publishes OCI feature artifacts |
| `release.yml` | push to main on `.devcontainer/**` | Template-inherited; path-gated to `.devcontainer/**` and SDK-unaware — DO NOT edit for SDK reasons |

## bazel-ci.yml (the SDK lane)

Single job `bazel` on `ubuntu-latest`, timeout 120 min. Steps in order:

1. `bazel-contrib/setup-bazel@…` — caches `bazelisk`, disk cache keyed on `.bazelrc`+`.bazelversion`+`MODULE.bazel`+all `go.mod`/`go.sum`, plus the repository cache.
2. **Drift check** — `bazel mod tidy && bazel run //:gazelle`, then `git diff --exit-code` AND a check for untracked `BUILD.bazel`/`go.mod`/`go.sum`. Catches both modifications and new files (post-audit finding #11).
3. `bazel build --config=ci //...`
4. `bazel test --config=ci //...` — race on, so every `//go:build !race` file is dropped at compile time. Step 5 is their only gate.
5. **Alloc + zero-alloc gates (race off)** — `bazel test --config=alloc <targets>`, where `<targets>` is read from `tools/alloc-lane-targets.txt`. That file is the single source of truth shared with `make test-alloc`; do NOT inline the list here.
6. **Exemption invariant** — `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if any `//go:build !race` test lives in a package the step-5 list does not cover. Without it, adding such a test to a new package silently produces a test that no lane runs (root `CLAUDE.md` rule 12).
7. `bazel coverage --combined_report=lcov //...` → uploaded as `coverage-${{ github.run_number }}` artifact (per-run unique name so concurrent runs don't dedupe, post-audit finding #28). Note coverage runs under the default (race-on) config, so it does **not** reflect the step-5 tests.

Concurrency: `${{ github.workflow }}-${{ github.ref }}` with cancel-in-progress.

## sdk-release.yml (the SDK release lane)

Single job `release` on `ubuntu-latest`, gated by `workflow_run` on `SDK CI (Bazel)` success. Steps:

1. `actions/checkout@93cb6efe…  # v5` with `fetch-depth: 0` — full history required so `git describe --tags` and `git worktree add <tag>` resolve.
2. `actions/setup-go@…` + `bazel-contrib/setup-bazel@…` — Bazel is needed for the `rdeps` query inside `compute-bumps.sh`.
3. Compute majors to bump (`scripts/release/compute-bumps.sh`, or `inputs.force_bumps` on manual dispatch).
4. Cut tags (`scripts/release/cut-tags.sh`) — strips `replace` lines, verifies `GOWORK=off go mod download`, race-protected re-read.
5. `gh release create --generate-notes --verify-tag` per pushed tag.
6. Upload `release-summary-${{ github.run_number }}` artifact — only when majors were bumped. The summary + upload steps are gated `if: always() && steps.compute.outputs.majors != ''`, so a no-op run (empty majors) skips both.

Concurrency: `sdk-release-${{ github.ref }}` with `cancel-in-progress: false` (NEVER cancel a tag-push mid-flight).

## Conventions

- Action references SHA-pinned with a trailing `# vX` comment (e.g. `actions/checkout@93cb6efe…  # v5`).
- `permissions: contents: read` only — nothing in the SDK lane writes back.
- `bazel-ci.yml` is the only source-of-truth gate. CI does NOT run `go test`, `golangci-lint`, or `govulncheck` directly anymore (ADR 0004).

## Do NOT

- Re-introduce a `go test` matrix here — drift between local Bazel and CI defeats ADR 0004's "single build system" decision.
- Drop the drift check; gazelle-generated `BUILD.bazel` files must be committed.
- Edit the template-inherited workflows here for SDK reasons.
