# Setup Validation

## Phase 5.0: Environment Validation

**Verify the environment (parallel via Task agents).**

```yaml
parallel_checks:
  agents:
    - name: "tools-checker"
      checks: [git, node, go, terraform, docker, grepai]
      output: "{tool, required, installed, status}"

    - name: "deps-checker"
      checks: [npm ci, go mod, terraform init]
      output: "{manager, status, issues}"

    - name: "config-checker"
      checks: [.env, CLAUDE.md, mcp.json]
      output: "{file, status, issue}"

    - name: "grepai-checker"
      checks: [Ollama, daemon, index]
      output: "{component, status, details}"

    - name: "secret-checker"
      checks: [op CLI, OP_SERVICE_ACCOUNT_TOKEN, vault access, project secrets]
      output: "{op_installed, token_set, vault_name, project_path, secrets_count, status}"
```

---

## Phase 6.0: Report

```
═══════════════════════════════════════════════════════════════
  /init - Complete
═══════════════════════════════════════════════════════════════

  Project: {name}
  Purpose: {purpose summary}

  Generated:
    ✓ docs/vision.md
    ✓ CLAUDE.md
    ✓ AGENTS.md
    ✓ docs/architecture.md
    ✓ docs/workflows.md
    ✓ README.md (updated)
    ✓ .coderabbit.yaml (generated if missing)
    ✓ .pr_agent.toml (generated if missing)
    ✓ .codacy.yaml (generated if missing)
    {{#if phase4_8_configured}}✓ Branch protection: main-protection ruleset (CI gates){{/if}}
    {conditional files}

  Environment:
    ✓ Tools installed ({tool list})
    ✓ Dependencies ready
    ✓ grepai indexed ({N} files)

  1Password:
    ✓ op CLI installed
    ✓ Vault connected ({N} project secrets)

  Ready to develop!
    → /feature "description" to start

═══════════════════════════════════════════════════════════════
```

---

## Phase 7.0: GrepAI Calibration

**MANDATORY** after project discovery. Calibrate grepai config based on project size and structure.

```yaml
grepai_calibration:
  1_count_files:
    command: |
      find /workspace -type f \
        -not -path '*/.git/*' -not -path '*/node_modules/*' \
        -not -path '*/vendor/*' -not -path '*/.grepai/*' \
        -not -path '*/__pycache__/*' -not -path '*/target/*' \
        -not -path '*/.venv/*' -not -path '*/dist/*' | wc -l
    output: file_count

  2_select_profile:
    rules:
      - "file_count < 10000   → profile: small"
      - "file_count < 100000  → profile: medium"
      - "file_count < 500000  → profile: large"
      - "file_count >= 500000 → profile: massive"

    profiles:
      small:
        chunking: { size: 1024, overlap: 100 }
        hybrid: { enabled: true, k: 60 }
        debounce_ms: 1000
      medium:
        chunking: { size: 1024, overlap: 100 }
        hybrid: { enabled: true, k: 60 }
        debounce_ms: 2000
      large:
        chunking: { size: 512, overlap: 50 }
        hybrid: { enabled: true, k: 60 }
        debounce_ms: 3000
      massive:
        chunking: { size: 512, overlap: 50 }
        hybrid: { enabled: false }
        debounce_ms: 5000

  3_detect_languages:
    action: "Scan for go.mod, package.json, Cargo.toml, etc."
    output: "Filter trace.enabled_languages to only detected languages"

  4_customize_boost:
    action: |
      Scan project structure (ls -d */):
      - If src/ exists → bonus /src/ 1.2
      - If pkg/ exists → bonus /pkg/ 1.15
      - If internal/ exists → bonus /internal/ 1.1
      - If app/ exists → bonus /app/ 1.15
      - If lib/ exists → bonus /lib/ 1.15
      Add project-specific ignore patterns (e.g., .next/, .nuxt/, .angular/)

  5_write_config:
    action: "Generate .grepai/config.yaml with selected profile"
    template: "/etc/grepai/config.yaml (base) + profile overrides"

  6_restart_daemon:
    action: |
      pkill -f 'grepai watch' 2>/dev/null || true
      rm -f /workspace/.grepai/index.gob /workspace/.grepai/symbols.gob
      nohup grepai watch >/tmp/grepai.log 2>&1 &
      sleep 3
      grepai status
```

**Output Phase 6:**

```
═══════════════════════════════════════════════════════════════
  GrepAI Calibration
═══════════════════════════════════════════════════════════════

  Files detected : 47,230
  Profile        : medium
  Model          : bge-m3 (1024d, 72% accuracy)

  Config applied:
    chunking    : 1024 tokens / 100 overlap
    hybrid      : ON (k=60)
    debounce    : 2000ms
    languages   : .go, .ts, .py (3 detected)

  Boost customized:
    +1.2  /src/
    +1.15 /pkg/
    +1.1  /internal/

  Daemon: restarted (indexing 47,230 files...)

═══════════════════════════════════════════════════════════════
```

---

## Auto-fix (automatic)

When a problem is detected, auto-fix if possible:

| Problem | Auto Action |
|---------|-------------|
| `.env` missing | `cp .env.example .env` |
| deps not installed | `npm ci` / `go mod download` |
| grepai not running | `nohup grepai watch &` |
| Ollama not reachable | Display HOST instructions |
| grepai uncalibrated | Run Phase 6 calibration |

---

## Guardrails

| Action | Status |
|--------|--------|
| Skip detection | FORBIDDEN |
| Closed questions / AskUserQuestion | FORBIDDEN |
| Placeholders in generated files | FORBIDDEN |
| Skip vision synthesis review | FORBIDDEN |
| Destructive fix without asking | FORBIDDEN |
