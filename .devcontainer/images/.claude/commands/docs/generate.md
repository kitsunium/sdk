# Content Generation & Serving (Phases 6.0-8.0)

## Phase 6.0: Content Generation

```yaml
phase_6_0_generate:
  description: "Generate documentation from consolidated results"

  rules:
    no_placeholders:
      - "NEVER write 'Coming Soon'"
      - "NEVER write 'TBD' or 'TODO'"
      - "NEVER create empty sections"

    content_requirements:
      - "Every page must have real content from agent analysis"
      - "Every code block must be functional"
      - "Every table must have data"
      - "Every architecture page must have at least one Mermaid diagram"
      - "Every flow description must have a sequence or flowchart diagram"

    link_not_copy:
      - "Reference source files via relative links"
      - "NEVER copy entire config files inline"
      - "Quote only the relevant excerpt (max 15 lines) with link to full file"

    editorial_rules:
      - "No generic filler: 'This module handles X' → explain HOW it handles X"
      - "Every section must contain information extractable ONLY from this project"
      - "Prefer 'The auth module exposes /login and /logout, uses JWT stored in Redis'"
      - "Over 'The auth module manages authentication'"

  #---------------------------------------------------------------------------
  # DOCUMENTATION PHILOSOPHY (Divio System)
  #---------------------------------------------------------------------------
  # Every page falls into one of four categories (never mix them):
  #   Tutorial    → learning-oriented, practical steps for beginners
  #   How-to      → task-oriented, steps for working developers
  #   Reference   → factual, structured lookup during active work
  #   Explanation → theoretical, conceptual understanding
  # Maintaining clear boundaries prevents "gravitational pull" toward
  # merging types, which degrades both author and reader experience.
  #---------------------------------------------------------------------------

  #---------------------------------------------------------------------------
  # UNIVERSAL TEMPLATES (applied to all project types)
  #---------------------------------------------------------------------------
  universal_templates:

    index_md:
      description: "Hero landing page with conditional feature section"
      generation_marker:
        first_line: '<!-- /docs-generated: {"date":"{TIMESTAMP}","commit":"{LAST_COMMIT_SHA}","pages":{TOTAL_PAGES},"agents":{N}} -->'
        rule: "ALWAYS insert as first line of index.md — enables freshness detection"
      structure:
        - "<!-- /docs-generated: {JSON_MARKER} -->"
        - "# {PROJECT_NAME}"
        - "{PROJECT_TAGLINE} — bold, one sentence"
        - "[ Get Started → ] button linking to #how-it-works anchor"
        - ""
        - "## Features (conditional on INTERNAL_PROJECT)"
        - "IF INTERNAL_PROJECT == true:"
        - "  Simple table: Feature | Description"
        - "  List all detected features with one-line descriptions"
        - "IF INTERNAL_PROJECT == false:"
        - "  Comparison table: Feature | {PROJECT_NAME} * | Competitor A | B | C"
        - "  Each cell: full support | partial | not available"
        - "  Include Price row"
        - "  Include Open Source row ONLY IF PUBLIC_REPO == true"
        - "  Competitors identified by agent analysis (contextually relevant)"
        - ""
        - "## How it works"
        - "{Mermaid flowchart: high-level system overview}"
        - "{2-3 sentences explaining the diagram}"
        - ""
        - "## Quick Start"
        - "{3-5 numbered steps to get running}"
        - ""
        - "--- footer ---"
        - "{PROJECT_NAME} · {LICENSE}"
        - "IF PUBLIC_REPO == true: · GitHub link to {GIT_REMOTE_URL}"
      conditional_rules:
        public_repo_false:
          - "No GitHub link in footer"
          - "No 'Open Source' row in comparison table"
        internal_project_true:
          - "Use simple Feature | Description table"
          - "No competitor columns, no comparison research"
        internal_project_false:
          - "Use comparison table with up to 3 competitors"
          - "Competitors contextually researched by agents"
      anti_patterns:
        - "Starting with technical details before the pitch"
        - "Listing features without explaining their benefit"
        - "Quick start that requires more than 5 steps"
        - "GitHub link when PUBLIC_REPO is false"
        - "Comparison table when INTERNAL_PROJECT is true"

    #---------------------------------------------------------------------------
    # C4 ARCHITECTURE TEMPLATES (Mermaid C4 diagrams)
    #---------------------------------------------------------------------------
    # Templates: .devcontainer/images/.claude/templates/docs/architecture/
    # Theme: docs/stylesheets/theme.css (derived from theme.css.tpl + accent_color)
    #
    # DECISION FRAMEWORK — which C4 levels to generate:
    #   ALWAYS: Level 1 (Context) + Level 2 (Container)
    #   CONDITIONAL: Level 3 (Component) — only if container has >5 modules
    #   CONDITIONAL: Dynamic — only for critical flows (max 3)
    #   CONDITIONAL: Deployment — only if infra signals detected
    #   NEVER: Level 4 (Code) — use IDE tools instead
    #
    # ELEMENT LIMITS:
    #   - Max 15 elements per diagram (split if more)
    #   - Every relationship has protocol label ("JSON/HTTPS", "JDBC")
    #   - Title format: "[Type] — {PROJECT_NAME}"
    #   - UpdateLayoutConfig($c4ShapeInRow="3", $c4BoundaryInRow="1")
    #
    # CROSS-LINKING with Transport page:
    #   - Container table Transport/Format columns link to transport.md#{anchor}
    #   - Communication Map links to transport.md for protocol details
    #---------------------------------------------------------------------------

    architecture_hub_md:
      description: "C4 hub page — progressive zoom navigation"
      template: "architecture/README.md.tpl"
      structure:
        - "# Architecture"
        - "Progressive zoom table: Level | Diagram | Audience | Focus"
        - "Mermaid legend diagram showing C4 element types"
        - "Links to context, container, component, dynamic, deployment"

    c4_context_md:
      description: "C4 Level 1 — System Context"
      condition: "ALWAYS generated"
      template: "architecture/c4-context.md.tpl"
      diagram_type: "C4Context"
      structure:
        - "# System Context"
        - "C4Context Mermaid diagram: Person, System, System_Ext, Rel"
        - "Key Interactions table: From | To | Protocol | Purpose"
        - "External Dependencies table: System | Type | Purpose | Criticality"
      rules:
        - "Exactly ONE internal System element (the project)"
        - "Every Rel has protocol label"
        - "Max 15 elements"

    c4_container_md:
      description: "C4 Level 2 — Container Diagram"
      condition: "ALWAYS generated"
      template: "architecture/c4-container.md.tpl"
      diagram_type: "C4Container"
      structure:
        - "# Container Diagram"
        - "C4Container Mermaid: System_Boundary, Container, ContainerDb, ContainerQueue"
        - "Containers table: Container | Technology | Responsibility | Transport | Format"
        - "Data Stores table: Store | Technology | Purpose | Access Pattern"
        - "Communication Map: Source | Destination | Protocol | Format | Direction"
      rules:
        - "All containers inside System_Boundary"
        - "Every container has Technology specified"
        - "Transport/Format columns link to transport.md"
        - "Integrate deployment details (don't create separate diagram unless complex)"

    c4_component_md:
      description: "C4 Level 3 — Component Diagram (conditional)"
      condition: "Container has >5 significant modules AND is critical path"
      template: "architecture/c4-component.md.tpl"
      diagram_type: "C4Component"
      structure:
        - "One section per qualifying container"
        - "C4Component Mermaid: Container_Boundary, Component, ComponentDb"
        - "Components table: Component | Technology | Responsibility | Key Files"
        - "Design Patterns table: Pattern | Where | Why"
      rules:
        - "Max 12 components per diagram"
        - "Focus on what's hard to discover from code"
        - "Reference ~/.claude/docs/ patterns when applicable"

    c4_dynamic_md:
      description: "C4 Dynamic — Critical flow diagrams"
      condition: "Critical user journeys or complex data flows detected"
      template: "architecture/c4-dynamic.md.tpl"
      diagram_type: "C4Dynamic"
      structure:
        - "One C4Dynamic per critical flow (max 3 flows)"
        - "Flow Steps table: Step | From | To | Action | Protocol"
        - "Error Scenarios table: Step | Condition | Response | HTTP Code"
      rules:
        - "Max 10 steps per flow"
        - "Number steps in Rel labels: '1. Submit credentials'"
        - "Show error/failure paths for critical flows"
        - "Statement order determines sequence (Mermaid ignores RelIndex)"

    c4_deployment_md:
      description: "C4 Deployment — Infrastructure topology"
      condition: "Deployment signals detected (docker-compose replicas, K8s, Terraform)"
      template: "architecture/c4-deployment.md.tpl"
      diagram_type: "C4Deployment"
      structure:
        - "C4Deployment Mermaid: Deployment_Node (nested), Container, ContainerDb"
        - "Infrastructure table: Node | Type | Spec | Containers"
        - "Scaling Strategy table: Aspect | Strategy | Details"
        - "Network table: Source | Destination | Port | Protocol | TLS"
        - "Recommended Configuration: Scenario | Nodes | CPU | RAM | Storage"
      rules:
        - "Max 3 nesting levels for Deployment_Node"
        - "Production environment only (not dev/staging)"
        - "Include replica counts"

  #---------------------------------------------------------------------------
  # MERMAID COLOR DIRECTIVES (non-C4 diagrams)
  #---------------------------------------------------------------------------
  # C4 diagrams are styled by theme.css (CSS) + UpdateElementStyle (inline).
  # Non-C4 diagrams (flowchart, sequence, state) need %%{init}%% + classDef.
  #---------------------------------------------------------------------------
  mermaid_color_directives:
    applies_to:
      - "flowchart"
      - "sequenceDiagram"
      - "stateDiagram-v2"
    does_NOT_apply_to:
      - "C4Context"
      - "C4Container"
      - "C4Component"
      - "C4Dynamic"
      - "C4Deployment"

    init_block: |
      %%{init: {'theme': 'dark', 'themeVariables': {
        'primaryColor': '{{COLOR_PRIMARY_BG}}',
        'primaryBorderColor': '{{COLOR_PRIMARY_BORDER}}',
        'primaryTextColor': '{{COLOR_TEXT}}',
        'lineColor': '{{COLOR_EDGE}}',
        'textColor': '{{COLOR_TEXT}}',
        'secondaryColor': '{{COLOR_DATA_BG}}',
        'secondaryBorderColor': '{{COLOR_DATA_BORDER}}',
        'secondaryTextColor': '{{COLOR_TEXT}}',
        'tertiaryColor': '{{COLOR_ASYNC_BG}}',
        'tertiaryBorderColor': '{{COLOR_ASYNC_BORDER}}',
        'tertiaryTextColor': '{{COLOR_TEXT}}',
        'noteBkgColor': '{{COLOR_LABEL_BG}}',
        'noteTextColor': '{{COLOR_TEXT}}',
        'noteBorderColor': '{{COLOR_EXTERNAL_BORDER}}',
        'actorBkg': '{{COLOR_PRIMARY_BG}}',
        'actorBorder': '{{COLOR_PRIMARY_BORDER}}',
        'actorTextColor': '{{COLOR_TEXT}}',
        'activationBkgColor': '{{COLOR_PRIMARY_BG}}',
        'activationBorderColor': '{{COLOR_PRIMARY_BORDER}}',
        'signalColor': '{{COLOR_EDGE}}',
        'signalTextColor': '{{COLOR_TEXT}}'
      }}}%%

    classDef_block: |
      classDef primary fill:{{COLOR_PRIMARY_BG}},stroke:{{COLOR_PRIMARY_BORDER}},color:{{COLOR_TEXT}}
      classDef data fill:{{COLOR_DATA_BG}},stroke:{{COLOR_DATA_BORDER}},color:{{COLOR_TEXT}}
      classDef async fill:{{COLOR_ASYNC_BG}},stroke:{{COLOR_ASYNC_BORDER}},color:{{COLOR_TEXT}}
      classDef external fill:{{COLOR_EXTERNAL_BG}},stroke:{{COLOR_EXTERNAL_BORDER}},color:{{COLOR_TEXT}}
      classDef error fill:{{COLOR_ERROR_BG}},stroke:{{COLOR_ERROR_BORDER}},color:{{COLOR_TEXT}}

    usage_rules:
      - "Every flowchart MUST start with the %%{init}%% block"
      - "Every flowchart MUST include classDef declarations"
      - "Assign semantic classes to nodes: A:::primary, B:::data, C:::async"
      - "Sequence and state diagrams need only %%{init}%% (no classDef)"
      - "The OVERVIEW_DIAGRAM in index.md MUST follow these rules"

    c4_inline_rules:
      - "Every C4 diagram MUST include UpdateElementStyle for each element"
      - "Mapping: Person/System/Container/Component → primary colors"
      - "Mapping: *Db → data colors, *Queue → async colors"
      - "Mapping: *_Ext → external colors"
      - "Mapping: error flows → error colors (in C4Dynamic)"
      - "Template: UpdateElementStyle(alias, $fontColor=\"{{COLOR_TEXT}}\", $bgColor=\"{{COLOR_*_BG}}\", $borderColor=\"{{COLOR_*_BORDER}}\")"
      - "Template: UpdateRelStyle(from, to, $textColor=\"{{COLOR_TEXT}}\", $lineColor=\"{{COLOR_EDGE}}\")"

  #---------------------------------------------------------------------------
  # PROJECT-TYPE SPECIFIC STRUCTURES
  #---------------------------------------------------------------------------
  generation_by_project_type:

    template:
      structure:
        index.md: "Product pitch: what this template provides, key features, quick start"
        getting-started/:
          README.md: "Installation methods (template, one-liner, manual)"
          workflow.md: "Feature development workflow with diagram"
          configuration.md: "Environment setup, tokens, MCP config"
        architecture/:
          README.md: "System context: DevContainer + Claude + MCP ecosystem"
          components.md: "Features, hooks, agents, commands — how they connect"
          flow.md: "Container lifecycle flow with sequence diagram"
        languages/:
          README.md: "Overview of all languages with comparison table"
          "{lang}.md": "One page per language: tools, linters, versions, why"
        commands/:
          README.md: "All commands overview with when-to-use decision tree"
          "{cmd}.md": "Full command doc: phases, args, examples, diagrams"
        agents/:
          README.md: "Agent ecosystem: orchestrators → specialists → executors"
          language-specialists.md: "All language agents with capabilities"
          devops-specialists.md: "All DevOps agents with domains"
          executors.md: "All executor agents with analysis types"
        automation/:
          README.md: "Automation overview: hooks + MCP + pre-commit"
          hooks.md: "All lifecycle hooks with execution order diagram"
          mcp-servers.md: "All MCP integrations with tools and auth"
        patterns/:
          README.md: "Design patterns KB: categories, counts, usage"
          by-category.md: "Patterns organized by category with links"
        reference/:
          conventions.md: "Coding conventions, commit format, branch naming"
          troubleshooting.md: "Common issues and solutions"

    library:
      structure:
        index.md: "Product pitch: what this library does, install, basic example"
        architecture/:
          README.md: "System context: library boundary and dependencies"
          components.md: "Internal module breakdown with diagram"
          flow.md: "Data flow through the library with sequence diagram"
        api/:
          README.md: "API overview: main types, functions, interfaces"
          "{module}.md": "Per-module: exported API, parameters, return types, examples"
        examples/:
          README.md: "Example index with difficulty levels"
          "{example}.md": "Each example: problem, solution, code, explanation"
        guides/:
          installation.md: "Installation and setup"
          usage.md: "Usage patterns and best practices"
          migration.md: "Version migration guide (if applicable)"

    application:
      structure:
        index.md: "Product pitch: what this app does, who it's for, quick start"
        architecture/:
          README.md: "Level 1: system context with C4 diagram"
          components.md: "Level 2: component breakdown with internal diagrams"
          flow.md: "Data flows with sequence diagrams per major flow"
          deployment.md: "Level 3: cluster, scaling, network (if applicable)"
          decisions.md: "Key architectural decisions with rationale"
        api/:
          README.md: "API overview: base URL, auth, rate limiting"
          endpoints.md: "All endpoints: method, path, request/response formats"
          protocols.md: "Communication protocols: HTTP, gRPC, WebSocket, etc."
        deployment/:
          README.md: "Deployment guide: prerequisites, steps"
          configuration.md: "All config options: env vars, files, secrets"
          cluster.md: "Cluster setup: nodes, replication, fault tolerance"
          network.md: "Network: ports, TLS, segmentation, load balancing"
        guides/:
          README.md: "User guides index"
          getting-started.md: "First steps after deployment"
          operations.md: "Day-to-day operations and maintenance"

  #---------------------------------------------------------------------------
  # COMMON PAGES (all project types)
  #---------------------------------------------------------------------------
  common_pages:
    transport.md:
      description: "Protocols and exchange formats — auto-detected from code"
      condition: "Always generated (at minimum documents HTTP/JSON)"
      template: ".devcontainer/images/.claude/templates/docs/transport.md.tpl"
      cross_linking:
        to_api: "Each 'Used by' cell links to api/{slug}.md"
        from_api: "API overview Transport/Format columns link back here"

    api/:
      overview.md:
        description: "API overview with transport cross-links"
        condition: "API_COUNT >= 1"
        template: ".devcontainer/images/.claude/templates/docs/api/overview.md.tpl"
      "{api_slug}.md":
        description: "Per-API detail page with endpoints"
        condition: "API_COUNT > 1 (one page per API)"
        template: ".devcontainer/images/.claude/templates/docs/api/detail.md.tpl"

    changelog.md:
      description: "Changelog from git conventional commits"
      condition: "Always generated"
      source: "git log --oneline with conventional commit parsing"
      structure:
        - "# Changelog"
        - "## [version] - date (grouped by feat/fix/docs/refactor)"

  #---------------------------------------------------------------------------
  # CROSS-LINKING RULES (Transport <-> API)
  #---------------------------------------------------------------------------
  cross_linking:
    transport_to_api:
      rule: "Each protocol/format 'Used by' cell links to relevant api/{slug}.md"
      anchor_convention: "protocol.toLowerCase() for transport anchors"
    api_to_transport:
      rule: "API overview Transport/Format columns link to transport.md#{anchor}"
      anchor_convention: "format.toLowerCase() for format anchors"
    slug_convention: "api_name.toLowerCase().replace(/\\s+/g, '-') for API slugs"
```

---

## Phase 8.0: Validation + Serve

```yaml
phase_8_0_validate_and_serve:
  description: "Final validation then start MkDocs server"

  validation:
    mandatory_checks:
      - "Every nav entry points to existing file"
      - "No file < 20 lines (likely placeholder)"
      - "No 'TODO', 'TBD', 'Coming Soon' in content"
      - "All code blocks have language tag"
      - "All internal links resolve"
      - "Every architecture page has at least one Mermaid diagram"
      - "index.md has generation marker as first line (<!-- /docs-generated: ... -->)"
      - "index.md starts with hero section after marker (not technical details)"
      - "No full config files copied inline (use links)"
      - "transport.md exists and has >= 1 protocol row"
      - "If API_COUNT >= 1: api/overview.md exists with endpoint table"
      - "If API_COUNT > 1: one api/{slug}.md per detected API"
      - "If PUBLIC_REPO == false: no repo_url in mkdocs.yml"
      - "If PUBLIC_REPO == false: no GitHub icon or tab in nav/footer"
      - "If INTERNAL_PROJECT == true: index.md has simple feature table (no comparison)"
      - "If INTERNAL_PROJECT == false: index.md has comparison table with competitors"
      - "Cross-links between transport.md and api/*.md pages resolve bidirectionally"

    warnings:
      - "File > 300 lines → suggest splitting"
      - "Architecture page without sequence diagram"
      - "API page without request/response examples"
      - "Deployment page without recommended config table"
      - "Transport page without protocol details subsections"

  serve:
    pre_check:
      - "pkill -f 'mkdocs serve' 2>/dev/null || true"

    command: "mkdocs serve -a 0.0.0.0:{PORT}"

    output_template: |
      ═══════════════════════════════════════════════════════════════
        /docs - Server Running (Deep Analysis Complete)
      ═══════════════════════════════════════════════════════════════

        Project Type: {PROJECT_TYPE}
        Analysis:     {N} agents completed
        Diagrams:     {D} Mermaid diagrams generated

        URL: http://localhost:{PORT}

        Generated Sections (by score):
        {SECTIONS_WITH_SCORES}

        Commands:
          /docs --update      Re-analyze and regenerate
          /docs --serve       (Re)start server
          /docs --stop        Stop server
          /docs --status      Show coverage stats

      ═══════════════════════════════════════════════════════════════
```

---

## Mode --serve

```yaml
serve:
  description: "(Re)start server with existing docs — no analysis, no regeneration"

  workflow:
    1_check_docs: "Verify docs/ exists with content (abort if empty)"
    2_kill_existing: "pkill -f 'mkdocs serve' 2>/dev/null || true"
    3_start_server: "mkdocs serve -a 0.0.0.0:{PORT}"

  use_case: "Restart server after manual doc edits, --stop, or port change"

  output_template: |
    ═══════════════════════════════════════════════════════════════
      /docs --serve - Server (Re)started
    ═══════════════════════════════════════════════════════════════

      URL: http://localhost:{PORT}

      Commands:
        /docs --update      Re-analyze and regenerate
        /docs --stop        Stop server
        /docs --status      Show coverage stats

    ═══════════════════════════════════════════════════════════════
```

**IF `$ARGUMENTS` contains `--serve`**: Execute Mode --serve and STOP (do not run analysis phases).

---

## Mode --stop

```yaml
stop:
  command: "pkill -f 'mkdocs serve'"
  output: "Server stopped. Restart: /docs --serve"
```

---

## Mode --status

```yaml
status:
  checks:
    - "Server running? (pgrep -f 'mkdocs serve')"
    - "Docs structure exists? (ls docs/)"
    - "Generation marker? (head -1 docs/index.md)"
    - "Git diff since marker commit"
    - "Content files count"
    - "Mermaid diagrams count"
    - "Broken internal links"
    - "Outdated dependency versions"

  output_template: |
    ═══════════════════════════════════════════════════════════════
      /docs - Status Report
    ═══════════════════════════════════════════════════════════════

      Server      : {RUNNING|STOPPED}
      Structure   : {EXISTS|MISSING}

      Freshness:
        Generated   : {TIMESTAMP} ({DAYS_AGO} days ago)
        Commit      : {MARKER_COMMIT} → HEAD ({COMMITS_SINCE} commits)
        Stale pages : {STALE_COUNT} / {TOTAL_PAGES}
        Broken links: {BROKEN_COUNT}
        Outdated    : {OUTDATED_COUNT} deps

      Content:
        Pages       : {TOTAL_PAGES} files
        Diagrams    : {D} Mermaid blocks
        Coverage    : {PERCENTAGE}%

      {IF_STALE: "Run /docs to update stale pages (incremental)"}
      {IF_FRESH: "Docs are up to date."}

    ═══════════════════════════════════════════════════════════════
```

---

## Mode --quick

**Alias for `--serve`.** Both skip analysis and (re)start the server with existing docs.

```yaml
quick:
  description: "Skip deep analysis, (re)start server with existing docs"
  alias_for: "--serve"

  workflow:
    1_check_docs: "Verify docs/ exists with content (abort if empty)"
    2_kill_existing: "pkill -f 'mkdocs serve' 2>/dev/null || true"
    3_start_server: "mkdocs serve -a 0.0.0.0:{PORT}"

  use_case: "Fast iteration when docs already generated"
```

---

## Guardrails (ABSOLUTE)

| Action | Status | Reason |
|--------|--------|--------|
| Create empty/placeholder page | **FORBIDDEN** | Broken UX |
| Launch agents sequentially | **FORBIDDEN** | Degraded performance |
| Skip scoring | **FORBIDDEN** | Loss of prioritization |
| Generate without analysis | **FORBIDDEN** | Superficial content |
| "Coming Soon" / "TBD" | **FORBIDDEN** | Empty promises |
| Create standalone section with score < 16 | **FORBIDDEN** | Navigation pollution |
| Ignore PROJECT_TYPE | **FORBIDDEN** | Unsuitable structure |
| Architecture page without diagram | **FORBIDDEN** | Degraded comprehension |
| Copy entire config file inline | **FORBIDDEN** | Desynchronization |
| Generic sentence without specific info | **FORBIDDEN** | Hollow content |
| index.md starting with technical content | **FORBIDDEN** | Product pitch first |
| Skip architecture-analyzer for app | **FORBIDDEN** | Architecture is critical |
| Skip freshness check (Phase 3.0) | **FORBIDDEN** | Unnecessary regeneration |
| Generate without marker in index.md | **FORBIDDEN** | Freshness impossible afterwards |
| Full regen when incremental suffices | **AVOID** | Waste of tokens/time |
| Skip Phase 1.0 (config questions) | **FORBIDDEN** | Config drives all conditional content |
| GitHub links when PUBLIC_REPO=false | **FORBIDDEN** | Private repo URL leak |
| Comparison table when INTERNAL_PROJECT=true | **FORBIDDEN** | No competitors for internal project |
| Simple table when INTERNAL_PROJECT=false | **FORBIDDEN** | Must show competitive advantage |
| API menu when API_COUNT=0 | **FORBIDDEN** | Empty nav section |
| Transport page without cross-links to API | **FORBIDDEN** | Cross-linking is the key feature |
| API page without cross-links to Transport | **FORBIDDEN** | Bidirectional is MANDATORY |
| Palette toggle in mkdocs.yml | **FORBIDDEN** | Dark-only, scheme: slate only |
| Hardcoded colors in C4 templates | **FORBIDDEN** | Use COLOR_* variables |
| c4-fix.css in mkdocs.yml | **FORBIDDEN** | Replaced by theme.css |
| Flowchart/sequence without %%{init}%% | **FORBIDDEN** | Inconsistent colors |
| C4 without UpdateElementStyle | **FORBIDDEN** | C4 ignores Mermaid themes |
| Background hex without "1a" suffix | **FORBIDDEN** | Pattern: border=full, bg=10% alpha |
| Skip accent_color question | **FORBIDDEN** | Color drives the entire theme |

---

## MkDocs Configuration

```yaml
# mkdocs.yml (generated at project root)
# See template: .devcontainer/images/.claude/templates/docs/mkdocs.yml.tpl
site_name: "{PROJECT_NAME}"
site_description: "{GENERATED_DESCRIPTION}"
docs_dir: docs

# CONDITIONAL — only if PUBLIC_REPO == true:
# repo_url: "{GIT_REMOTE_URL}"
# repo_name: "{REPO_NAME}"
# edit_uri: "edit/main/docs/"

theme:
  name: material
  palette:
    scheme: slate
    primary: custom
    accent: custom
  features:
    - navigation.tabs
    - navigation.sections
    - navigation.expand
    - navigation.top
    - search.suggest
    - search.highlight
    - content.code.copy
    - content.tabs.link
  # CONDITIONAL — only if PUBLIC_REPO == true:
  # icon:
  #   repo: fontawesome/brands/github

plugins:
  - search

markdown_extensions:
  - pymdownx.highlight:
      anchor_linenums: true
      line_spans: __span
      pygments_lang_class: true
  - pymdownx.inlinehilite
  - pymdownx.snippets
  - pymdownx.superfences:
      custom_fences:
        - name: mermaid
          class: mermaid
          format: !!python/name:pymdownx.superfences.fence_code_format
  - pymdownx.tabbed:
      alternate_style: true
  - pymdownx.details
  - pymdownx.emoji:
      emoji_index: !!python/name:material.extensions.emoji.twemoji
      emoji_generator: !!python/name:material.extensions.emoji.to_svg
  - admonition
  - attr_list
  - md_in_html
  - tables
  - toc:
      permalink: true

nav:
  # GENERATED by nav_algorithm — never hand-edited
  # ---
  # nav_algorithm:
  #   1. "Docs" tab: index.md + scored sections from Phase 6.0
  #   2. "Transport" tab: transport.md (always present)
  #   3. API tab (conditional on API_COUNT):
  #      - API_COUNT == 0 → no nav item
  #      - API_COUNT == 1 → "API: api/overview.md" (direct link)
  #      - API_COUNT > 1  → "APIs:" dropdown with Overview + per-API pages
  #   4. "Changelog" tab: changelog.md (always present)
  #   5. "GitHub" tab: external link to GIT_REMOTE_URL (only if PUBLIC_REPO == true)
  #   6. Validate: every nav entry points to an existing file
  #
  # Example output (public repo, external project, 2 APIs):
  #   - Docs:
  #     - Home: index.md
  #     - Architecture:
  #       - Overview: architecture/README.md
  #       - Components: architecture/components.md
  #   - Transport: transport.md
  #   - APIs:
  #     - Overview: api/overview.md
  #     - HTTP API: api/http-api.md
  #     - Raft API: api/raft-api.md
  #   - Changelog: changelog.md
  #   - GitHub: https://github.com/org/repo

extra_css:
  - stylesheets/theme.css

extra:
  generator: false
  # CONDITIONAL — only if PUBLIC_REPO == true:
  # social:
  #   - icon: fontawesome/brands/github
  #     link: "{GIT_REMOTE_URL}"

# CONDITIONAL copyright:
#   PUBLIC_REPO true:  "{PROJECT_NAME} · {LICENSE} · <a href='{GIT_REMOTE_URL}'>GitHub</a>"
#   PUBLIC_REPO false: "{PROJECT_NAME} · {LICENSE}"
```

---

## Error Messages

```yaml
errors:
  analysis_failed:
    message: |
      Analysis agent failed: {AGENT_NAME}

      Error: {ERROR_MESSAGE}

      Continuing with partial results...

  empty_section_detected:
    message: |
      Empty section detected: {SECTION}

      Score: {SCORE}/43

      This section has no real content.
      Moving to reference section.

  missing_diagram:
    message: |
      Architecture page without diagram: {PAGE}

      Score: {SCORE}/43 (includes +3 diagram bonus)

      Generating Mermaid diagram from agent analysis data.
      The diagram uses real component names from the codebase.
```

---

## Sources and References

This skill's design draws from the following methodologies and research:

| Source | Contribution | Reference |
|--------|-------------|-----------|
| **C4 Model** (Simon Brown) | Progressive architecture zoom (Context → Container → Component), diagram practices | [Practical C4 Modeling Tips](https://revision.app/blog/practical-c4-modeling-tips) |
| **DocAgent** (arXiv 2504.08725) | Multi-agent coordination: Reader → Searcher → Writer → Verifier, dependency-first ordering, iterative feedback loops | [DocAgent: Multi-Agent Collaboration](https://arxiv.org/html/2504.08725v1) |
| **Divio Documentation System** | Four documentation types (Tutorial, How-to, Reference, Explanation), boundary maintenance | [Divio Documentation Structure](https://docs.divio.com/documentation-system/structure/) |
| **MkDocs** | Configuration options: repo_url, edit_uri, nav structure, validation, plugins | [MkDocs Configuration Guide](https://www.mkdocs.org/user-guide/configuration/) |
| **MkDocs Material** | Theme: dark/light palette, navigation features, code copy, search, Mermaid diagrams | [Material for MkDocs](https://squidfunk.github.io/mkdocs-material/) |
| **Product Documentation Tips** | Entry point pattern (About/Access/Usage/Resources/Support), chunking, navigation depth | [10 Tips for Product Documentation](https://developerhub.io/blog/10-tips-for-structuring-your-product-documentation/) |
| **Mermaid** | Diagram types: flowchart, sequence, C4 context, ER, state machine, deployment | [Mermaid Documentation](https://mermaid.js.org/) |
