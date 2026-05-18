<!-- updated: 2026-05-18T14:30:00Z -->
# .github/

## Purpose

GitHub-side configuration for the SDK repo: the Bazel CI lane and Dependabot. The remaining workflows (`docker-images.yml`, `publish-features.yml`, `release.yml`) are inherited from the devcontainer-template parent repo and path-gated on `.devcontainer/**` so they do not fire on SDK PRs.

## Contents

| Path | Role |
|---|---|
| `workflows/bazel-ci.yml`        | SDK CI lane (drift check, build, test, coverage) — see `workflows/CLAUDE.md` |
| `workflows/docker-images.yml`   | Template-inherited; gated on `.devcontainer/images/**` |
| `workflows/publish-features.yml`| Template-inherited; gated on `.devcontainer/features/**` |
| `workflows/release.yml`         | Template-inherited; gated on `.devcontainer/**` |
| `dependabot.yml`                | Weekly `github-actions` ecosystem updates, `chore:` commit prefix, `dependencies` label |

## Conventions

- `ubuntu-latest` runners; action references SHA-pinned with a trailing `# vX` version comment.
- `permissions: contents: read` at the workflow root unless a step needs more.
- Concurrency group `${{ github.workflow }}-${{ github.ref }}` with `cancel-in-progress: true` so rapid re-pushes do not corrupt Bazel caches.
- Authenticate via `GITHUB_TOKEN`; never inline secrets.

## Do NOT

- Add a non-Bazel CI job for SDK code. The source of truth is `bazel test --config=race //...`.
- Edit the template-inherited workflows here for SDK reasons — patch them upstream.
- Remove the path-gates on the inherited workflows; that fires the template builds on every SDK PR.

## Subtree

- `workflows/` — see `workflows/CLAUDE.md`
