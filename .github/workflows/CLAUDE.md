<!-- updated: 2026-04-10T12:00:00Z -->
# GitHub Actions Workflows

## Purpose

CI/CD automation. Primary workflow is `sdk-ci.yml` (Go tests + lint +
vulncheck). The remaining workflows (`docker-images.yml`,
`publish-features.yml`, `release.yml`) are inherited from the
devcontainer-template parent repo and are path-gated on `.devcontainer/**`
so they do not fire on SDK PRs.

## Workflows

| File | Description |
|------|-------------|
| `sdk-ci.yml` | Primary CI for this repo — Go tests, lint, govulncheck across all 4 SDK modules |
| `docker-images.yml` | Build and push devcontainer images (path-gated on `.devcontainer/images/**`) |
| `publish-features.yml` | Publish Dev Container Features as OCI artifacts (path-gated on `.devcontainer/features/**`) |
| `release.yml` | Create GitHub Release with claude-assets.tar.gz (path-gated on `.devcontainer/**`) |

## docker-images.yml (Two-Tier Build)

**Base image** (`devcontainer-base`):
- **Trigger**: Weekly (Sunday 3AM UTC), `[base]` in commit message, manual dispatch
- **Content**: apt, PPA tools, Cloud CLIs, MkDocs, Oh My Zsh (~1.1GB, stable)

**Main image** (`devcontainer-template`):
- **Trigger**: Push to main, PRs, daily (4AM UTC), ktn-linter-release
- **Content**: kubectl, grepai, rtk, Claude Code, CodeRabbit, Qodo (~120MB delta)

- **Registry**: ghcr.io
- **Tags**: latest, date, commit SHA
- **Platforms**: linux/amd64, linux/arm64
- **Cache busting**: Scheduled builds pass `CACHE_BUST_DYNAMIC=YYYY-MM-DD`

## publish-features.yml

- **Trigger**: Push to main (features changed), workflow_dispatch
- **Action**: Flattens features, embeds shared utils, publishes as OCI artifacts
- **Registry**: `ghcr.io/kodflow/devcontainer-features/<feature>:v<version>`
- **Uses**: `devcontainers/action@v1`

## release.yml

- **Trigger**: Push to main, workflow_dispatch
- **Action**: Generates `claude-assets.tar.gz` and creates a GitHub Release
- **Tag format**: `vYYYY.MM.DD-<sha7>`
- **Latest**: Always marks as latest release (used by `install.sh`)

## sdk-ci.yml

- **Trigger**: Push to main, PRs — path-gated on `internal/**`, `pkg/**`, `go.work`, `go.mod`, `.golangci.yml`, `Makefile`
- **Jobs**:
  - `test`: `go test -race -cover` per module (matrix: kernel, core, service, pkg/v1)
  - `lint`: `golangci-lint run` with depguard layer firewall
  - `vulncheck`: `govulncheck ./...` per module
- **Runs with** `GOWORK=off` so each module is tested as an external consumer via its own `go.mod` + `replace` directives.

## Conventions

- Use `ubuntu-latest` runners
- Cache Docker layers for speed
- Action SHAs pinned with version comments
- Use GITHUB_TOKEN for authentication
