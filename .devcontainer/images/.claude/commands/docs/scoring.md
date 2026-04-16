# Consolidation, Scoring & Verification (Phases 5.0-7.0)

## Phase 5.0: Consolidation and Scoring

```yaml
phase_5_0_consolidation:
  description: "Merge agent results and apply enhanced scoring"

  scoring_formula:
    base: "complexity + usage + uniqueness + gap"
    diagram_bonus: "+3 if complexity >= 7 AND no diagram exists yet"
    total_max: 43
    thresholds:
      primary: ">= 24 (full page + mandatory Mermaid diagram)"
      standard: "16-23 (own page, diagram recommended)"
      reference: "< 16 (aggregated in reference section)"

  diagram_requirement:
    rule: |
      IF score >= 24 AND component is architectural:
        MUST include at least one Mermaid diagram
      IF score >= 24 AND component has data flow:
        MUST include sequence or flowchart diagram
      IF cluster/scaling detected:
        MUST include deployment diagram

  # DocAgent-inspired: dependencies-first ordering ensures components are
  # documented only after their dependencies have been processed.
  dependency_ordering:
    rule: "Topological sort of component dependencies before content generation"
    reason: "A module's docs can reference its dependency's docs via links"
    implementation:
      - "Build dependency DAG from agent results (imports, calls, data flow)"
      - "Topological sort → processing order for Phase 6.0"
      - "Earlier components provide context for later ones"

  consolidation_steps:
    1_collect:
      action: "Read all JSON files from /tmp/docs-analysis/*.json"

    2_deduplicate:
      action: "Merge overlapping information (structure + architecture)"

    3_score:
      action: "Calculate total score per component with diagram bonus"

    4_prioritize:
      action: "Sort by score descending, then by dependency order"

    5_identify_diagrams:
      action: "For each primary section, determine required diagram types"

    5b_cross_link_transport_api:
      action: |
        Build cross-reference maps between APIs and Transports:
        - For each API: resolve its transport protocol and exchange format
        - For each Transport: list all APIs that use it
        - For each Format: list all APIs that use it
        These maps drive the "Used by" columns in transport.md
        and the "Transport/Format" columns in api/overview.md

    5c_persist_apis:
      action: |
        Update ~/.claude/docs/config.json apis array with detected APIs:
        Read existing config → merge apis field → write back.
        This enables subsequent incremental runs to skip full API detection.

    6_structure:
      action: "Build documentation tree adapted to PROJECT_TYPE"

  output_structure:
    common:
      - "index.md (always — hero + conditional features)"
      - "transport.md (always — auto-detected protocols and formats)"
      - "api/ (conditional: only if API_COUNT > 0)"
      - "architecture/ (if application/library, primary: score >= 24)"
    template:
      - "getting-started/ (primary: score >= 24)"
      - "languages/ (primary: score >= 24)"
      - "commands/ (primary: score >= 24)"
      - "agents/ (standard: score >= 16)"
      - "automation/ (hooks + mcp, standard: score >= 16)"
      - "patterns/ (if KB exists, standard: score >= 16)"
      - "reference/ (aggregated: score < 16)"
    application:
      - "architecture/ (always for app)"
      - "api/ (if endpoints detected)"
      - "deployment/ (if cluster/docker detected)"
      - "guides/ (standard: score >= 16)"
      - "reference/ (aggregated: score < 16)"
    library:
      - "architecture/ (if complex internal structure)"
      - "api/ (always for library)"
      - "examples/ (standard: score >= 16)"
      - "guides/ (standard: score >= 16)"
      - "reference/ (aggregated: score < 16)"
```

---

## Phase 7.0: Verification (DocAgent-inspired)

```yaml
phase_7_0_verify:
  description: "Iterative quality verification before serving"
  inspiration: "DocAgent multi-agent pattern: Writer → Verifier feedback loop"
  max_iterations: 2

  verifier_checks:
    completeness:
      - "Every primary section (score >= 24) has a full page"
      - "Every standard section (score >= 16) has an own page"
      - "No section references information not present in agent results"
    accuracy:
      - "Mermaid diagrams match actual component names from code"
      - "File paths in links point to real files"
      - "Version numbers match what install scripts actually install"
    quality:
      - "No generic filler ('This module handles X' without explaining HOW)"
      - "Every table has >= 2 rows of real data"
      - "Every code block is syntactically valid"
    no_placeholders:
      - "No 'Coming Soon', 'TBD', 'TODO', 'WIP' in any page"
      - "No '{VARIABLE}' patterns remaining in generated content"
      - "No empty sections or stub pages"
    cross_linking:
      - "Every Transport column in api/overview.md links to valid transport.md anchor"
      - "Every 'Used by' cell in transport.md links to valid api/*.md page"
      - "GitHub links only present when PUBLIC_REPO == true"
      - "Comparison table only present when INTERNAL_PROJECT == false"
      - "Simple feature table only present when INTERNAL_PROJECT == true"
    config_consistency:
      - "~/.claude/docs/config.json exists and contains public_repo + internal_project"
      - "apis[] array matches detected APIs in generated pages"
      - "mkdocs.yml repo_url present only if PUBLIC_REPO == true"
      - "mkdocs.yml nav has no GitHub tab if PUBLIC_REPO == false"

  feedback_loop:
    on_failure:
      action: "Fix the specific issue and re-verify (up to max_iterations)"
      strategy: "Targeted fix — only regenerate the failing section, not all docs"
    on_success:
      action: "Proceed to Phase 8.0 (Serve)"
    on_max_iterations:
      action: "Proceed with warnings listed in serve output"
```
