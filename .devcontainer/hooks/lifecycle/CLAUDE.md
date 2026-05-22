<!-- updated: 2026-05-12T09:29:19Z -->
# Lifecycle Hooks

## Purpose

`initialize.sh` runs on the **host machine** before container build.
All other lifecycle hooks are image-embedded at `/etc/devcontainer-hooks/lifecycle/`.
`post-poststart-fixup.sh` is a temporary consumer-side patch — see below.

## Scripts

| Script | Event | Runs on | Description |
|--------|-------|---------|-------------|
| `initialize.sh` | initializeCommand | Host | .env setup, Ollama install, feature validation |
| `post-poststart-fixup.sh` | postStartCommand (chained) | Container | Idempotent patches for rtk hook duplicate + missing Go MCP fragment. Remove once upstream ships the fixes. |

## initialize.sh

- Generates `.env` from `.env.example` (project name from git remote)
- Validates feature structure (install.sh + devcontainer-feature.json)
- Pulls latest Docker image to bypass cache

## Execution Order

1. `initialize.sh` (host, before build)
2. `/etc/devcontainer-hooks/lifecycle/onCreate.sh` (in container)
3. `/etc/devcontainer-hooks/lifecycle/postCreate.sh` (in container)
4. `/etc/devcontainer-hooks/lifecycle/postStart.sh` (in container, each start)
5. `/etc/devcontainer-hooks/lifecycle/postAttach.sh` (in container, VS Code attach)
