# Phase 1.0: Approved Plan Detection

## Path Resolution (MANDATORY)

All `.claude/` paths MUST be absolute, anchored to workspace root:
```bash
WORKSPACE_ROOT=$(git rev-parse --show-toplevel 2>/dev/null || echo /workspace)
```
- Glob plans from: `${WORKSPACE_ROOT}/.claude/plans/*.md`
- Read contexts from: `${WORKSPACE_ROOT}/.claude/contexts/{slug}.md`

**NEVER use relative `.claude/` paths** — subagents may operate from subdirectories.

---

**Announce:** Always start with: "I'm using /do to {task_summary}"

**ALWAYS execute first. Checks if /plan was used.**

```yaml
plan_detection:
  check: "Does an approved plan exist in context or on disk?"

  sources:
    - "Recent conversation (plan validated by user)"
    - "Claude session memory"
    - ".claude/plans/*.md (disk-persisted plans)"

  detection_workflow:
    1_check_explicit_flag:
      condition: "--plan <path> argument provided"
      action: "Read the specified plan file directly"
      priority: "HIGHEST"

    2_check_conversation:
      condition: "Plan visible in conversation context"
      action: "Use plan from conversation"
      priority: "HIGH"
      signals:
        - "User said 'yes', 'ok', 'go', 'approved' after a /plan"
        - "Structured plan with numbered steps visible"
        - "ExitPlanMode was called successfully"

    3_check_disk_plans:
      condition: "No plan found in conversation context"
      action: "Glob .claude/plans/*.md, read most recent"
      priority: "MEDIUM (conversation is fresher than disk)"

  priority_rule: "Explicit flag > Conversation > Disk"

  if_plan_found:
    mode: "PLAN_EXECUTION"
    actions:
      - "Extract: title, steps[], scope, files[]"
      - "Check for 'Context:' header line in plan"
      - "If context path found → Read .claude/contexts/{slug}.md"
      - "Load discoveries, relevant_files, implementation_notes into working memory"
      - "Skip Phase 0 (interactive questions)"
      - "Use plan steps as sub-objectives"
      - "Criteria = plan completed + tests/lint/build pass"

  context_recovery:
    trigger: "Plan file contains 'Context: .claude/contexts/{slug}.md' header"
    workflow:
      1_extract_path: "Parse 'Context:' line from plan header"
      2_read_context: "Read .claude/contexts/{slug}.md if exists"
      3_load_sections:
        discoveries: "Key findings from planning phase"
        relevant_files: "Files to focus on"
        implementation_notes: "Technical decisions and constraints"
      4_graceful_degradation: "If context file missing → warn and proceed without"
    purpose: "Restore full planning context after 'clear context' or compaction"

  if_no_plan:
    mode: "ITERATIVE"
    actions:
      - "Continue to Phase 0 (questions)"
```

**Output Phase 1.0 (plan detected):**

```
═══════════════════════════════════════════════════════════════
  /do - Plan Detection
═══════════════════════════════════════════════════════════════

  ✓ Approved plan detected!

  Source : conversation | .claude/plans/{slug}.md
  Plan   : "Add JWT authentication to API"
  Context: .claude/contexts/{slug}.md (loaded)
  Steps  : 4
  Scope  : src/auth/, src/middleware/
  Files  : 6 to modify, 2 to create

  Mode: PLAN_EXECUTION (skipping interactive questions)

  Proceeding to Phase 4.0 (Peek)...

═══════════════════════════════════════════════════════════════
```

**Output Phase 1.0 (no plan):**

```
═══════════════════════════════════════════════════════════════
  /do - Plan Detection
═══════════════════════════════════════════════════════════════

  No approved plan found.

  Mode: ITERATIVE (interactive questions required)

  Proceeding to Phase 3.0 (Questions)...

═══════════════════════════════════════════════════════════════
```

---

## Phase 2.0: Secret Discovery (1Password)

**Check if secrets are available for this project:**

```yaml
secret_discovery:
  trigger: "ALWAYS (before Phase 0)"
  blocking: false  # Informational only

  1_check_available:
    condition: "command -v op && test -n $OP_SERVICE_ACCOUNT_TOKEN"
    on_failure: "Skip silently (1Password not configured)"

  2_resolve_path:
    action: "Extract org/repo from git remote origin"
    command: |
      REMOTE=$(git config --get remote.origin.url)
      # Extract org/repo from HTTPS, SSH, or token-embedded URLs
      PROJECT_PATH=$(echo "${REMOTE%.git}" | grep -oP '[:/]\K[^/]+/[^/]+$')

  3_list_project_secrets:
    action: "List project secrets"
    command: |
      op item list --vault='$VAULT_ID' --format=json \
        | jq -r '.[] | select(.title | startswith("'$PROJECT_PATH'/")) | .title'
    extract: "Remove prefix to keep key names only"

  4_check_task_needs:
    action: "If the task mentions secret/token/credential/password/API key"
    match_keywords: ["secret", "token", "credential", "password", "api key", "api_key", "auth"]
    if_match_and_secrets_exist:
      output: |
        ═══════════════════════════════════════════════════════════════
          /do - Secrets Available
        ═══════════════════════════════════════════════════════════════

          Project: {PROJECT_PATH}
          Available secrets in 1Password:
            ├─ DB_PASSWORD
            ├─ API_KEY
            └─ JWT_SECRET

          Use /secret --get <key> to retrieve a value
          These may help with the current task.

        ═══════════════════════════════════════════════════════════════
    if_no_secrets:
      output: "(no project secrets in 1Password, continuing...)"
```
