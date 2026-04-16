<!-- updated: 2026-03-26T18:00:00Z -->
# Lifecycle Hooks

## Purpose

Only `initialize.sh` lives here — runs on the **host machine** before container build.
All other lifecycle hooks are image-embedded at `/etc/devcontainer-hooks/lifecycle/`.

## Scripts

| Script | Event | Runs on | Description |
|--------|-------|---------|-------------|
| `initialize.sh` | initializeCommand | Host | .env setup, Ollama install, feature validation |

## initialize.sh

- Generates `.env` from `.env.example` (project name from git remote)
- Validates feature structure (install.sh + devcontainer-feature.json)
- Installs/starts Ollama on host with `OLLAMA_HOST=0.0.0.0` (container-accessible)
- Pulls embedding model (`bge-m3`) for grepai semantic search
- Pulls latest Docker image to bypass cache

## Execution Order

1. `initialize.sh` (host, before build)
2. `/etc/devcontainer-hooks/lifecycle/onCreate.sh` (in container)
3. `/etc/devcontainer-hooks/lifecycle/postCreate.sh` (in container)
4. `/etc/devcontainer-hooks/lifecycle/postStart.sh` (in container, each start)
5. `/etc/devcontainer-hooks/lifecycle/postAttach.sh` (in container, VS Code attach)
