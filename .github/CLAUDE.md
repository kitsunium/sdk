<!-- updated: 2026-09-28T19:19:15Z -->
# .github/

## Purpose

GitHub-side configuration for the SDK repo: the workflows — the SDK lanes, the scans, the runtime suites, the docs and the benchmarks, each described in `workflows/CLAUDE.md` — and Dependabot. No workflow is inherited from a template any more: the template's `release.yml` was deleted in #260, and its image build went with the devcontainer itself (ADR 0153).

## Contents

| Path | Role |
|---|---|
| `workflows/bazel-ci.yml`        | SDK CI lane (drift check, build, test, alloc lane + exemption guard, lint, vulnerability gate, coverage, cross-build and 386 matrices) — see `workflows/CLAUDE.md` |
| `workflows/post-commit.yml`     | The other required check: no AI attribution in the history, conventional subjects, no credentials |
| `workflows/sdk-release.yml`     | Tags a release after a green `SDK CI (Bazel)` run on `main` |
| `workflows/release-size.yml`    | The size a pull request would publish, asked before it merges |
| `workflows/vuln-scan.yml`       | Daily govulncheck over every module |
| `workflows/e2e-cross.yml`       | Runtime tests on Linux, macOS, Windows and the BSDs |
| `workflows/e2e-vm.yml`          | Manual runs on the lab's persistent VMs |
| `workflows/docs-deploy.yml`     | Builds and deploys the versioned docs portal |
| `workflows/bazel-bench.yml`     | Kernel benchmarks, on demand, weekly or on a `run-bench` label |
| `dependabot.yml`                | Weekly `github-actions` ecosystem updates, `chore:` commit prefix, `dependencies` label |

## Conventions

- `ubuntu-latest` runners, except where the platform is the point (`e2e-cross.yml`'s macOS and Windows cells — its BSDs run in VMs on `ubuntu-latest` — and `e2e-vm.yml`'s `kitsunium-runner` scale set); action references SHA-pinned with a trailing `# vX` version comment, except `kodflow/post-commit@main`, unpinned on purpose.
- `permissions: contents: read` at the workflow root unless a step needs more.
- Concurrency group `${{ github.workflow }}-${{ github.ref }}` with `cancel-in-progress: true` so rapid re-pushes do not corrupt Bazel caches — except where a run must never be cut short: `sdk-release.yml` (a tag push in flight), `docs-deploy.yml` and `e2e-vm.yml` (one shared lab) do not cancel.
- Authenticate via `GITHUB_TOKEN`, or a repository secret where that token cannot reach (`KTN_LINTER_TOKEN` for the private linter, the lab's secrets in `e2e-vm.yml`); never inline secrets.

## Do NOT

- Add a `go test` lane for a platform Bazel already covers. The source of truth is `bazel test --config=race //...`; `cross-build` and `test-386` run raw `go` only because Bazel here builds for the host (see `workflows/CLAUDE.md` §Do NOT).
- Bring a template's workflows back from a sync — `release.yml` (#260) or the devcontainer image build (ADR 0153). Neither built anything this repository ships.

## Subtree

- `workflows/` — see `workflows/CLAUDE.md`
