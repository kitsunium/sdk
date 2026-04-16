<!-- updated: 2026-04-10T12:00:00Z -->
# DevContainer Features

## Purpose

Modular features for languages, tools, and architectures.

## Structure

```text
features/
├── languages/        # 25 languages + shared utility library
│   ├── shared/       # feature-utils.sh (colors, logging, arch, GitHub API)
│   └── <lang>/       # install.sh + devcontainer-feature.json
├── architectures/    # Architecture patterns (14 patterns)
├── browser/          # Playwright browser testing
├── claude/           # Claude Code standalone integration
├── infrastructure/   # Terragrunt, TFLint, Infracost, cfssl, Ansible tools
└── kubernetes/       # Local K8s via kind
```

## Key Components

- Each language has `install.sh` + `devcontainer-feature.json`
- All install.sh source `shared/feature-utils.sh` (with inline fallback)
- Downloads parallelized with `&` + `wait` for faster builds
- Conventions enforced by specialist agents (e.g., `developer-specialist-go`)

## MCP Integration

Features can contribute MCP server configs via fragment files:

1. Add `mcp.json` in the feature directory (e.g., `languages/go/mcp.json`)
2. Call `install_mcp_fragment "$FEATURE_DIR"` at end of `install.sh`
3. Fragment is copied to `/etc/mcp/features/<name>.mcp.json` at build time
4. `postStart.sh` merges fragments into `/workspace/mcp.json` at runtime
5. `requires_binary` field gates inclusion (skipped if binary not found)

Fragment format:
```json
{
  "servers": {
    "server-name": {
      "command": "binary",
      "args": ["arg1"],
      "env": {},
      "requires_binary": "binary"
    }
  }
}
```

## Adding a Language

1. Create `languages/<name>/`
2. Add `devcontainer-feature.json` for metadata
3. Add `install.sh` sourcing `shared/feature-utils.sh`
4. (Optional) Add `mcp.json` if the language provides an MCP server
