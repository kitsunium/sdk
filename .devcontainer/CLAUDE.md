<!-- updated: 2026-04-10T12:00:00Z -->
# DevContainer Configuration

## Purpose

Development container setup for consistent dev environments across languages.

## Structure

```text
.devcontainer/
├── devcontainer.json    # Main config
├── docker-compose.yml   # Multi-service setup
├── Dockerfile           # Extends images/ base
├── install.sh           # Standalone Claude installer
├── scripts/             # Build utilities
│   └── generate-assets-archive.sh
├── features/            # Language & tool features
│   ├── languages/       # 25 languages + shared/
│   ├── architectures/   # 14 architecture patterns
│   ├── browser/         # Playwright browser feature
│   ├── claude/          # Standalone Claude feature
│   ├── infrastructure/  # Terragrunt, TFLint, Infracost
│   └── kubernetes/      # Local K8s via kind
├── hooks/               # Host-side only (initialize.sh) + project extensions
├── tests/               # Unit tests (BATS)
└── images/              # Two-tier Docker images + Claude config
    ├── Dockerfile.base  # Stable layer (apt, Cloud CLIs) — weekly
    └── Dockerfile       # Dynamic layer (Claude, tools) — daily
```

## Key Files

- `devcontainer.json`: VS Code devcontainer config
- `docker-compose.yml`: Services (app, MCP servers)
- `.env`: Environment variables (git-ignored)
- `scripts/generate-assets-archive.sh`: Generates
  `claude-assets.tar.gz` (published via GitHub Releases)
- `scripts/list-team-agents.sh`: Deterministic extraction
  of agents referenced by team-migrated skills
- `install.sh`: Installs Claude CLI + tmux + detects
  Agent Teams capability. `--no-teams` to force-disable.

## Usage

Features are enabled in `devcontainer.json` under `features`.
Language conventions are enforced by specialist agents (e.g., `developer-specialist-go`).
