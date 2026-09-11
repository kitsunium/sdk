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

Three jobs: `bazel` (the gate), `cross-build` (every module COMPILES on every
supported GOOS/GOARCH) and `test-386` (the 32-bit RUNTIME). The last two run raw
`go` rather than Bazel, for the reason stated under Do NOT below: Bazel here
builds for the host only, so a platform it cannot reach is covered by the
toolchain that can, or by nothing.

Job `bazel` on `ubuntu-latest`, timeout 120 min. Steps in order:

1. `bazel-contrib/setup-bazel@…` — caches `bazelisk`, disk cache keyed on `.bazelrc`+`.bazelversion`+`MODULE.bazel`+all `go.mod`/`go.sum`, plus the repository cache.
2. **Drift check** — `bazel mod tidy && bazel run //:gazelle`, then `git diff --exit-code` AND a check for untracked `BUILD.bazel`/`go.mod`/`go.sum`. Catches both modifications and new files (post-audit finding #11).
3. `bazel build --config=ci //...`
4. `bazel test --config=ci //...` — race on, so every `//go:build !race` file is dropped at compile time. Step 5 is their only gate.
5. **Alloc + zero-alloc gates (race off)** — `bazel test --config=alloc <targets>`, where `<targets>` is read from `tools/alloc-lane-targets.txt`. That file is the single source of truth shared with `make test-alloc`; do NOT inline the list here.
6. **Exemption invariant** — `scripts/pre-commit/check-alloc-lane-coverage.sh` fails the build if any `//go:build !race` test lives in a package the step-5 list does not cover. Without it, adding such a test to a new package silently produces a test that no lane runs (root `CLAUDE.md` rule 12).
8. **Domain-doc invariant** — `scripts/pre-commit/check-domain-docs.sh` fails the build when the root `CLAUDE.md`'s architecture tree no longer names exactly the directories under `internal/core`, when a domain is described twice in the Purpose paragraph, or when two Purpose paragraphs coexist — and, since the same class of drift reached the ADR indexes, when `docs/adr/CLAUDE.md` or the root Reference list stops naming exactly the ADRs on disk, once each. Every one of those had actually happened.
7. **Audit-coverage invariant** — `scripts/pre-commit/check-audit-coverage.sh` fails the build when a package declaring an `errs.Define` or `errs.Code` is absent from `//:audit_sources`. The errs AST audits can only judge the files staged as their runfiles, so such a package is audited by nothing AND passes green — the worst shape a verification gap can take.
7. `bazel coverage --combined_report=lcov //...` → uploaded as `coverage-${{ github.run_number }}` artifact (per-run unique name so concurrent runs don't dedupe, post-audit finding #28). Note coverage runs under the default (race-on) config, so it does **not** reflect the step-5 tests.

Concurrency: `${{ github.workflow }}-${{ github.ref }}` with cancel-in-progress.

### cross-build (the build bar) and test-386 (the runtime bar)

`cross-build` runs `go build ./...` per module across eleven GOOS/GOARCH cells,
including linux/386 and linux/arm, so a platform-specific low-level call can
never silently drop a package (ADR 0018's build bar; local equivalent
`bash scripts/cross-platform-audit.sh`).

`test-386` RUNS the four workspace modules' tests on linux/386. Compiling and
behaving are different questions, and the gap between them is where a 64-bit
assumption survives: `int(0xffffffff)` is `-1` where `int` is 32 bits and passes
every `> cap` check, which is exactly the bound `internal/service/session`'s
at-rest frame relies on — two of its malformed frames can only fail there.
Adding the job found two defects immediately: a bench helper that did not
compile (`Statfs_t.Type` is `int32` on 386 and `int64` elsewhere) and a test
synthesising a slice longer than a 32-bit `len` can hold.

Linux/amd64 runners execute 386 binaries natively, so there is no emulation. It
runs without `-race`, which has no 386 support at all — which also makes it the
SECOND lane to compile the `//go:build !race` files, after the alloc lane.

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

- Re-introduce a `go test` lane for a platform **Bazel already covers**. That is
  the drift ADR 0004's "single build system" decision exists to prevent: two
  lanes running the same tests on the same platform, disagreeing, with nobody
  sure which is authoritative. A lane for a platform Bazel CANNOT build for is
  the opposite case — `cross-build` and `test-386` exist precisely because
  Bazel here builds for the host, and the alternative to raw `go` there is no
  coverage at all.
- Drop the drift check; gazelle-generated `BUILD.bazel` files must be committed.
- Edit the template-inherited workflows here for SDK reasons.
