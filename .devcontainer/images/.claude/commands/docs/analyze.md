# Multi-Agent Analysis (Phase 4.0)

## Phase 4.0: Parallel Analysis Agents (File-Based Dispatch)

**Architecture:** Each analyzer is a separate agent file in `.claude/agents/docs-analyzer-*.md`
with `context: fork` (isolated context) and file-based output to `/tmp/docs-analysis/`.
This prevents context saturation — each agent writes JSON results to disk and returns
a 1-line summary to the main context.

**INCREMENTAL MODE:** Only launch agents whose scope covers stale pages.

### Setup

Create output directory before launching agents:

```bash
mkdir -p /tmp/docs-analysis
```

### Phase 4.1: Category Analyzers (8 parallel haiku agents)

**CRITICAL:** Launch ALL 8 agents in a SINGLE message with multiple Task calls.

```yaml
phase_4_1_dispatch:
  agents:
    - subagent_type: "docs-analyzer-languages"
      max_turns: 12
      prompt: "Analyze language features. Write JSON to /tmp/docs-analysis/languages.json."
      trigger: "PROJECT_TYPE in [template, library, application]"

    - subagent_type: "docs-analyzer-commands"
      max_turns: 12
      prompt: "Analyze Claude commands/skills. Write JSON to /tmp/docs-analysis/commands.json."
      trigger: "Always"

    - subagent_type: "docs-analyzer-agents"
      max_turns: 12
      prompt: "Analyze specialist agents. Write JSON to /tmp/docs-analysis/agents.json."
      trigger: "PROJECT_TYPE == template OR .claude/agents/ exists"

    - subagent_type: "docs-analyzer-hooks"
      max_turns: 12
      prompt: "Analyze lifecycle and Claude hooks. Write JSON to /tmp/docs-analysis/hooks.json."
      trigger: "PROJECT_TYPE == template OR .devcontainer/hooks/ exists"

    - subagent_type: "docs-analyzer-mcp"
      max_turns: 10
      prompt: "Analyze MCP server configuration. Write JSON to /tmp/docs-analysis/mcp.json."
      trigger: "mcp.json exists"

    - subagent_type: "docs-analyzer-patterns"
      max_turns: 10
      prompt: "Analyze design patterns KB. Write JSON to /tmp/docs-analysis/patterns.json."
      trigger: "~/.claude/docs/ exists"

    - subagent_type: "docs-analyzer-structure"
      max_turns: 10
      prompt: "Map project structure. Write JSON to /tmp/docs-analysis/structure.json."
      trigger: "Always"

    - subagent_type: "docs-analyzer-config"
      max_turns: 10
      prompt: "Analyze configuration and env vars. Write JSON to /tmp/docs-analysis/config.json."
      trigger: "Always"

  output: "Each agent writes JSON to /tmp/docs-analysis/{name}.json"
  return: "Each agent returns 1-line: 'DONE: {name} - N items, score X/10'"
  wait: "ALL Phase 4.1 agents must complete before Phase 4.2"
```

### Phase 4.2: Architecture Analyzer (1 sonnet agent)

**Runs AFTER Phase 4.1 completes.** The architecture analyzer reads Phase 4.1 JSON results
from `/tmp/docs-analysis/` to gain project context before performing deep analysis.

```yaml
phase_4_2_dispatch:
  agent:
    subagent_type: "docs-analyzer-architecture"
    max_turns: 20
    prompt: |
      Deep architecture analysis. Phase 4.1 results are available in
      /tmp/docs-analysis/*.json — read them first for project context.
      Write your results to /tmp/docs-analysis/architecture.json.
    trigger: "PROJECT_TYPE in [library, application] OR src/ exists"

  output: "/tmp/docs-analysis/architecture.json"
  return: "1-line: 'DONE: architecture - N components, M APIs, score X/10'"
```

### Agent File Reference

| Agent File | Model | Max Turns | Scope |
|------------|-------|-----------|-------|
| `docs-analyzer-languages.md` | haiku | 12 | `.devcontainer/features/languages/` |
| `docs-analyzer-commands.md` | haiku | 12 | `.claude/commands/` |
| `docs-analyzer-agents.md` | haiku | 12 | `.claude/agents/` |
| `docs-analyzer-hooks.md` | haiku | 12 | `.devcontainer/hooks/` |
| `docs-analyzer-mcp.md` | haiku | 10 | `mcp.json`, `mcp.json.tpl` |
| `docs-analyzer-patterns.md` | haiku | 10 | `~/.claude/docs/` |
| `docs-analyzer-structure.md` | haiku | 10 | Project root (depth 3) |
| `docs-analyzer-config.md` | haiku | 10 | `.env`, `devcontainer.json`, `docker-compose.yml` |
| `docs-analyzer-architecture.md` | sonnet | 20 | `src/`, APIs, data flows, C4 diagrams |

### Output Format

All agents write to `/tmp/docs-analysis/{name}.json` with structure:

```json
{
  "agent": "{name}",
  "...": "agent-specific data",
  "scoring": {"complexity": 7, "usage": 9, "uniqueness": 8, "gap": 6},
  "summary": "One-line summary"
}
```
