---
name: docs
description: |
  Documentation Server with Deep Analysis (RLM Multi-Agent).
  Launches N parallel agents to analyze every aspect of the project.
  Scoring mechanism identifies what's important to document.
  Adapts structure based on project type (template vs application).
allowed-tools:
  - "Read(**/*)"
  - "Glob(**/*)"
  - "Grep(**/*)"
  - "Write(docs/**)"
  - "Write(mkdocs.yml)"
  - "Write(~/.claude/docs/config.json)"
  - "Task(*)"
  - "Bash(mkdocs:*)"
  - "Bash(cd:*)"
  - "Bash(mkdir:*)"
  - "Bash(kill:*)"
  - "Bash(pgrep:*)"
  - "Bash(pkill:*)"
  - "Bash(curl:*)"
  - "Bash(sleep:*)"
  - "mcp__grepai__grepai_search"
  - "mcp__grepai__grepai_trace_callers"
  - "mcp__grepai__grepai_trace_callees"
  - "mcp__grepai__grepai_trace_graph"
  - "mcp__context7__*"
  - "AskUserQuestion"
---

# /docs - Documentation Server (Deep Analysis)

## GREPAI-FIRST (MANDATORY)

Use `grepai_search` for ALL semantic/meaning-based queries BEFORE Grep.
Use `grepai_trace_callers`/`grepai_trace_callees` for impact analysis.
Fallback to Grep ONLY for exact string matches or regex patterns.

## CONTEXT7 (RECOMMENDED)

Use `mcp__context7__resolve-library-id` + `mcp__context7__query-docs` to:
- Fetch up-to-date API references for libraries used in the project
- Verify framework documentation accuracy before generating docs

Generate and serve **comprehensive** project documentation using MkDocs Material.

**Key Difference:** This skill launches **N parallel analysis agents**, each specialized
for a different aspect (languages, commands, hooks, agents, architecture, etc.).
Results are scored and consolidated into real, useful documentation with Mermaid
diagrams, concrete examples, and progressive architecture zoom.

$ARGUMENTS

---

## Execution Mode Detection (Agent Teams)

@.devcontainer/images/.claude/commands/shared/team-mode.md

Before Phase 4.1 (parallel analysis), determine runtime mode:

```bash
source "$HOME/.claude/scripts/team-mode-primitives.sh"
MODE=$(detect_runtime_mode)
```

Branch:
- `TEAMS_TMUX` / `TEAMS_INPROCESS` → **TEAMS analysis dispatch** (2 waves of 4 teammates)
- `SUBAGENTS` → legacy 8-agent parallel dispatch from Phase 4.1 (unchanged)

### TEAMS analysis dispatch

Lead: `docs-analyzer-architecture`. The 8 `docs-analyzer-*` agents exceed the 5-teammate hard cap, so split into **2 sequential waves of 4**:

```text
Wave 1 (TaskCreate × 4):
  docs-agents    → using docs-analyzer-agents
  docs-commands  → using docs-analyzer-commands
  docs-hooks     → using docs-analyzer-hooks
  docs-config    → using docs-analyzer-config

Wait for TeammateIdle × 4 → feed wave-1 JSON into wave-2 prompts

Wave 2 (TaskCreate × 4):
  docs-mcp       → using docs-analyzer-mcp
  docs-patterns  → using docs-analyzer-patterns
  docs-structure → using docs-analyzer-structure
  docs-languages → using docs-analyzer-languages
```

All 8 tasks use `access_mode: "read-only"` and `owned_paths: []`. Synthesis in Phase 5.0 merges all 8 JSON outputs using the existing scoring logic. Token ceiling ≤ 3x legacy (breadth justifies the extra cost).

---

## Core Principles

```yaml
principles:
  deep_analysis:
    rule: "Launch N agents with context: fork, each writing JSON to /tmp/docs-analysis/"
    iterations: "Phase 4.1: 8 haiku agents parallel, Phase 4.2: 1 sonnet agent with context"
    output: "File-based JSON results with scoring, 1-line summaries in main context"

  no_superficial_content:
    rule: "NEVER list without explaining"
    bad: "Available commands: /git, /review, /plan"
    good: "### /git - Full workflow with phases, arguments, examples"

  product_pitch_first:
    rule: "index.md MUST answer 'What problem does this solve?' before anything technical"
    structure: "Problem → Solution → Key features → Quick start → What's inside → Support"
    reason: "Readers decide in 30 seconds if the project is relevant to them"
    entry_point: "Landing page provides: About, Access, Usage, Resources, Support"

  progressive_zoom:
    rule: "Architecture docs follow Google Maps analogy: macro → micro"
    levels:
      - "Level 1: System context — big blocks, external dependencies"
      - "Level 2: Components — modules, services, their roles"
      - "Level 3: Internal — implementation details, algorithms, data structures"
    reason: "Reader picks the zoom level they need"

  diagrams_mandatory:
    rule: "Every architecture or flow page MUST include at least one Mermaid diagram"
    types: ["flowchart", "sequence", "C4 context", "ER diagram", "state machine"]
    reason: "Visual comprehension is 60,000x faster than text"

  link_dont_copy:
    rule: "Reference source files via links, not inline copies"
    bad: "```yaml\n# Copy of docker-compose.yml\nservices:\n  app: ...\n```"
    good: "See [`docker-compose.yml`](../docker-compose.yml) for full service definition."
    reason: "Copied content desynchronizes immediately"

  project_specific:
    rule: "Every analysis is unique to THIS project"
    reason: "Questions asked for template != questions for app using template"

  scoring_mechanism:
    rule: "Identify what's IMPORTANT to surface"
    criteria:
      - "Complexity (1-10): How complex is this component?"
      - "Usage frequency (1-10): How often will users need this?"
      - "Uniqueness (1-10): How specific to this project?"
      - "Documentation gap (1-10): How underdocumented currently?"
      - "Diagram bonus (+3): complexity >= 7 AND no diagram exists yet"
    thresholds:
      primary: "Score >= 24 → full page + mandatory diagram"
      standard: "Score 16-23 → own page, diagram recommended"
      reference: "Score < 16 → aggregated in reference section"

  adaptive_structure:
    template_project: "How to use, languages, commands, agents, hooks"
    library_project: "API reference, usage examples, integration guides, internal architecture"
    application_project: "Architecture, API, deployment, data flow, cluster, configuration"
```

---

## Arguments

| Argument | Action |
|----------|--------|
| (none) | Freshness check → incremental or full analysis → serve on :8080 |
| `--update` | Force full re-analysis, ignore freshness (regenerate everything) |
| `--serve` | (Re)start server with existing docs (kill + restart, no analysis) |
| `--stop` | Stop running MkDocs server |
| `--status` | Show freshness report, stale pages, server status |
| `--port <n>` | Custom port (default: 8080) |
| `--quick` | Skip analysis entirely, serve existing docs as-is |
| `--help` | Show help |

---

## --help

```
═══════════════════════════════════════════════════════════════
  /docs - Documentation Server (Deep Analysis)
═══════════════════════════════════════════════════════════════

  DESCRIPTION
    Generates comprehensive documentation using multi-agent
    parallel analysis. Each agent specializes in one aspect
    (languages, commands, hooks, architecture, etc.). Results
    are scored and consolidated into rich documentation with
    Mermaid diagrams and progressive architecture zoom.

  USAGE
    /docs [OPTIONS]

  OPTIONS
    (none)              Freshness check + incremental/full + serve
    --update            Force full regeneration (ignore freshness)
    --serve             (Re)start server (kill existing + restart, no analysis)
    --stop              Stop running MkDocs server
    --status            Freshness report + stale pages + server status
    --port <n>          Custom port (default: 8080)
    --quick             Serve existing docs as-is, skip analysis
    --help              Show this help

  ANALYSIS AGENTS (file-based, context: fork)
    Phase 4.1 (8 haiku agents, parallel → /tmp/docs-analysis/):
      languages     install.sh → tools, versions, why
      commands      commands/*.md → workflows, args
      agents        agents/*.md → capabilities
      hooks         lifecycle/*.sh → automation
      mcp           mcp.json → integrations
      patterns      ~/.claude/docs/ → patterns KB
      structure     codebase → directory map
      config        env, settings → configuration
    Phase 4.2 (1 sonnet agent, reads Phase 4.1 results):
      architecture  code → components, flows, protocols

  SCORING (what to document)
    Complexity:     1-10 (how complex?)
    Usage:          1-10 (how often needed?)
    Uniqueness:     1-10 (how project-specific?)
    Gap:            1-10 (how underdocumented?)
    Diagram bonus:  +3 (complexity >= 7, no diagram yet)
    Total >= 24     → Primary (full page + mandatory diagram)
    Total 16-23     → Standard (own page, diagram recommended)
    Total < 16      → Reference (aggregated)

  EXAMPLES
    /docs                   # Freshness check → update stale pages → serve
    /docs --update          # Force full regeneration from scratch
    /docs --serve           # (Re)start server after manual edits or --stop
    /docs --status          # Show what's stale without regenerating
    /docs --quick           # Serve existing docs immediately
    /docs --stop            # Stop server

═══════════════════════════════════════════════════════════════
```

**IF `$ARGUMENTS` contains `--help`**: Display the help above and STOP.

---

## Template Variables

All `{VARIABLE}` placeholders used in output templates and generated files:

```yaml
variables:
  # Derived from project analysis (Phase 2.0-4.0)
  PROJECT_NAME: "From CLAUDE.md title, package.json name, go.mod module, or git repo name"
  PROJECT_TYPE: "template | library | application | empty (detected in Phase 2.0)"
  GENERATED_DESCRIPTION: "2-3 sentence summary synthesized from agent analysis results"

  # Derived from scoring (Phase 5.0)
  SECTIONS_WITH_SCORES: "Formatted list: section name + score, sorted descending"

  # Derived from agent results (Phase 4.0)
  N: "Number of analysis agents that completed successfully"
  D: "Total count of Mermaid diagrams generated across all pages"

  # Derived from git (Phase 2.0)
  GIT_REMOTE_URL: "From git remote get-url origin (for repo_url in mkdocs.yml)"
  REPO_NAME: "Auto-detected from remote URL host (GitHub/GitLab/Bitbucket)"

  # Freshness (Phase 3.0)
  LAST_COMMIT_SHA: "Git SHA from generation marker in docs/index.md"
  MARKER_DATE: "ISO8601 date from generation marker"
  MARKER_COMMIT: "Short SHA from generation marker"
  DAYS_AGO: "Days since last generation"
  COMMITS_SINCE: "Number of commits between marker SHA and HEAD"
  CHANGED_COUNT: "Number of files changed since marker commit"
  STALE_COUNT: "Number of doc pages affected by changed files"
  STALE_LIST: "Formatted list of stale page paths"
  BROKEN_COUNT: "Number of broken internal links in docs/"
  BROKEN_LIST: "Formatted list of broken links with source page"
  OUTDATED_COUNT: "Number of dependency version mismatches"
  OUTDATED_LIST: "Formatted list: dep name, docs version vs actual version"
  TOTAL_PAGES: "Total number of pages in docs/"

  # From Phase 1.0 config (~/.claude/docs/config.json)
  PUBLIC_REPO: "Boolean — controls GitHub links in header/footer/nav and repo_url in mkdocs.yml"
  INTERNAL_PROJECT: "Boolean — controls feature table style (simple vs comparison)"

  # From architecture-analyzer (Phase 4.0, stored in config)
  APIS: "Array of {name, path, method, transport, format, description}"
  API_COUNT: "len(APIS) — controls nav: 0=hidden, 1='API' direct, N='APIs' dropdown"
  TRANSPORTS: "Array of {protocol, direction, port, tls, used_by_apis[]}"
  FORMATS: "Array of {name, content_type, used_by_apis[], deduced: boolean}"
  PROJECT_TAGLINE: "One-sentence tagline synthesized from analysis"

  # Color system (derived from accent_color via color_derivation algorithm)
  ACCENT_HEX: "hex from config accent_color (e.g. '#df41fb')"
  COLOR_PRIMARY_BORDER: "ACCENT_HEX (e.g. '#df41fb')"
  COLOR_PRIMARY_BG: "ACCENT_HEX + '1a' (10% alpha, e.g. '#df41fb1a')"
  COLOR_DATA_BORDER: "triadic left: hsl_to_hex((H-120+360)%360, S, L)"
  COLOR_DATA_BG: "COLOR_DATA_BORDER + '1a' (10% alpha)"
  COLOR_ASYNC_BORDER: "triadic right: hsl_to_hex((H+120)%360, S, L)"
  COLOR_ASYNC_BG: "COLOR_ASYNC_BORDER + '1a' (10% alpha)"
  COLOR_EXTERNAL_BORDER: "'#6c7693' (fixed desaturated gray)"
  COLOR_EXTERNAL_BG: "'#6c76931a' (fixed)"
  COLOR_ERROR_BORDER: "'#e83030' (fixed red)"
  COLOR_ERROR_BG: "'#e830301a' (fixed)"
  COLOR_TEXT: "'#d4d8e0' (constant — light text for dark mode)"
  COLOR_LABEL_BG: "'#1e2129' (constant — slate dark)"
  COLOR_EDGE: "'#d4d8e0' (constant — same as text)"

  # User-configurable
  PORT: "MkDocs serve port (default: 8080, override with --port)"

  # Runtime
  RUNNING: "pgrep -f 'mkdocs serve' returns 0"
  STOPPED: "pgrep -f 'mkdocs serve' returns non-zero"
  TIMESTAMP: "date -Iseconds of last analysis run"
  PERCENTAGE: "(pages_with_content / total_nav_entries) * 100"
```

---

## Color Derivation Algorithm

Given `accent_color` from Phase 1.0 config, derive the full semantic palette:

```yaml
color_derivation:
  input: "ACCENT_HEX from ~/.claude/docs/config.json accent_color"

  algorithm:
    1_parse_hsl: "Convert ACCENT_HEX to HSL (H, S, L)"
    2_primary:
      border: "ACCENT_HEX (unchanged)"
      background: "ACCENT_HEX + '1a' (append 10% alpha suffix)"
    3_data_triadic_left:
      border: "hsl_to_hex((H - 120 + 360) % 360, S, L)"
      background: "data_border + '1a'"
    4_async_triadic_right:
      border: "hsl_to_hex((H + 120) % 360, S, L)"
      background: "async_border + '1a'"
    5_fixed_roles:
      external: { border: "#6c7693", background: "#6c76931a" }
      error: { border: "#e83030", background: "#e830301a" }
    6_constants:
      text: "#d4d8e0"
      label_bg: "#1e2129"
      edge: "#d4d8e0"

  preset_table:
    "#9D76FB":  { data: "#76fb9d", async: "#fb9d76" }  # Purple (default)
    "#6BA3FF":  { data: "#a3ff6b", async: "#ff6ba3" }  # Blue
    "#4DD0E1":  { data: "#d0e14d", async: "#e14dd0" }  # Teal
    "#66BB6A":  { data: "#bb6a66", async: "#6a66bb" }  # Green
    "#FFB74D":  { data: "#b74dff", async: "#4dffb7" }  # Orange

  semantic_mapping:
    Person: "primary"
    System: "primary"
    Container: "primary"
    Component: "primary"
    System_Ext: "external"
    Person_Ext: "external"
    ContainerDb: "data"
    ComponentDb: "data"
    ContainerQueue: "async"
    ComponentQueue: "async"
    Deployment_Node: "external (border only, fill #2d2d2d)"

  application_layers:
    css_theme: "theme.css.tpl → stylesheets/theme.css (MkDocs + C4 global)"
    init_block: "%%{init}%% directive in flowchart/sequence/state diagrams"
    classDef: "classDef declarations in flowcharts for semantic node roles"
    UpdateElementStyle: "Per-element inline in C4 diagrams (belt-and-suspenders with CSS)"
```

---

## Architecture Overview

```
/docs Execution Flow
────────────────────────────────────────────────────────────────

Phase 1.0: Configuration Gate
├─ Read ~/.claude/docs/config.json
├─ If missing/incomplete: ask 3 mandatory questions (AskUserQuestion)
│   ├─ Q1: "Is this repository public?"  → public_repo
│   ├─ Q2: "Is this an internal project?" → internal_project
│   └─ Q3: "Choose your accent color"    → accent_color
├─ Persist answers to ~/.claude/docs/config.json
├─ Derive semantic color palette from accent_color (triadic HSL)
└─ Load config into template variables (PUBLIC_REPO, INTERNAL_PROJECT, ACCENT_COLOR)

Phase 2.0: Project Detection
├─ Detect project type (template/library/app/empty)
└─ Choose analysis strategy + agent list

Phase 3.0: Freshness Check
├─ Read generation marker from docs/index.md
├─ git diff <last_sha>..HEAD → changed files
├─ Map changed files → stale doc pages
├─ Check broken links + outdated deps
└─ Decision: INCREMENTAL (stale pages only) or FULL

Phase 4.1: Category Analyzers (8 haiku agents, ONE message)
├─ Task(docs-analyzer-languages)   ──┐
├─ Task(docs-analyzer-commands)    ──┤
├─ Task(docs-analyzer-agents)      ──┤
├─ Task(docs-analyzer-hooks)       ──┤ ALL PARALLEL
├─ Task(docs-analyzer-mcp)         ──┤ → /tmp/docs-analysis/*.json
├─ Task(docs-analyzer-patterns)    ──┤
├─ Task(docs-analyzer-structure)   ──┤
└─ Task(docs-analyzer-config)      ──┘

Phase 4.2: Architecture Analyzer (1 sonnet agent, reads Phase 4.1)
└─ Task(docs-analyzer-architecture) → /tmp/docs-analysis/architecture.json

Phase 5.0: Consolidation + Scoring
├─ Read all JSON from /tmp/docs-analysis/
├─ Build dependency DAG, topological sort
├─ Apply scoring formula (with diagram bonus)
├─ Identify high-priority sections
└─ Build documentation tree

Phase 6.0: Content Generation (dependency order)
├─ Generate index.md (product pitch first)
├─ For each scored section:
│   ├─ If score >= 24: Primary — full page + mandatory diagram
│   ├─ If score 16-23: Standard — own page, diagram recommended
│   └─ If score < 16: Reference — aggregated in reference section
├─ Generate architecture pages (C4 progressive zoom)
└─ Generate nav structure

Phase 7.0: Verification (DocAgent-inspired)
├─ Verify: completeness, accuracy, quality, no placeholders
├─ Feedback loop: fix issues and re-verify (max 2 iterations)
└─ Proceed with warnings if max iterations reached

Phase 8.0: Serve
├─ Final checks
└─ Start MkDocs on specified port
```

---

## Phase Modules

| Phase | Module | Description |
|-------|--------|-------------|
| 1.0-3.0 | Read ~/.claude/commands/docs/scan.md | Configuration gate, project detection, freshness check |
| 4.0 | Read ~/.claude/commands/docs/analyze.md | Multi-agent parallel analysis (8 haiku + 1 sonnet) |
| 5.0-7.0 | Read ~/.claude/commands/docs/scoring.md | Consolidation, scoring formula, verification loop |
| 6.0-8.0 | Read ~/.claude/commands/docs/generate.md | Content generation, serving, modes, guardrails, MkDocs config |
