#!/bin/bash
# ============================================================================
# git-guard.sh - Combined git commit/push guard (PreToolUse Bash)
# Merges commit-validate.sh + security.sh into a single script.
#
# Only activates on git commit/push commands. All other Bash commands
# pass through instantly (no JSON parsing overhead).
#
# Functions:
#   1. Block AI/Claude mentions in commit messages
#   2. Auto-correct --force → --force-with-lease
#   3. Scan staged files for secrets before commit
#
# Exit 0 = allow, Exit 2 = block
# ============================================================================

set +e  # Fail-open

# === Read hook input ===
INPUT="$(cat 2>/dev/null || true)"
if [ -z "$INPUT" ] || ! command -v jq &>/dev/null; then
    exit 0
fi

TOOL=$(printf '%s' "$INPUT" | jq -r '.tool_name // ""' 2>/dev/null || echo "")
COMMAND=$(printf '%s' "$INPUT" | jq -r '.tool_input.command // ""' 2>/dev/null || echo "")

# Only handle Bash tool
if [ "$TOOL" != "Bash" ]; then
    exit 0
fi

# Normalize RTK-prefixed commands
NORMALIZED_CMD="$COMMAND"
if [[ "$NORMALIZED_CMD" =~ ^rtk[[:space:]]+proxy[[:space:]]+ ]]; then
    NORMALIZED_CMD="${NORMALIZED_CMD#rtk proxy }"
elif [[ "$NORMALIZED_CMD" =~ ^rtk[[:space:]]+ ]]; then
    NORMALIZED_CMD="${NORMALIZED_CMD#rtk }"
fi

# === FAST EXIT: not a git commit/push? Skip entirely ===
if [[ ! "$NORMALIZED_CMD" =~ ^git[[:space:]]+(commit|push) ]]; then
    exit 0
fi

# ============================================================
# From here: git commit or git push only
# ============================================================

# === 1. Auto-correct --force → --force-with-lease (push) ===
if [[ "$NORMALIZED_CMD" =~ ^git[[:space:]]+push ]] && \
   [[ "$NORMALIZED_CMD" =~ --force ]] && \
   [[ ! "$NORMALIZED_CMD" =~ --force-with-lease ]]; then
    # Replace standalone --force with --force-with-lease (preserves quoting)
    CORRECTED="${COMMAND/--force/--force-with-lease}"
    echo "⚠️  Auto-corrected: --force → --force-with-lease" >&2
    jq -n --arg cmd "$CORRECTED" \
        --arg reason "Auto-corrected: --force → --force-with-lease" \
        '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":$reason,"updatedInput":{"command":$cmd}}}'
    exit 0
fi

# === 2. Block AI mentions in commit messages ===
if [[ "$NORMALIZED_CMD" =~ ^git[[:space:]]+commit ]]; then
    # Extract commit message
    COMMIT_MSG=""
    if [[ "$COMMAND" =~ -m[[:space:]]+[\"\']([^\"]+)[\"\'] ]]; then
        COMMIT_MSG="${BASH_REMATCH[1]}"
    fi
    if [[ "$COMMAND" =~ cat[[:space:]]+\<\<[\'\"]?EOF ]]; then
        COMMIT_MSG=$(echo "$COMMAND" | sed -n '/<<.*EOF/,/EOF/p' | grep -v "EOF" | grep -v "cat <<")
    fi
    if [[ "$COMMAND" =~ --message=[\"\']([^\"]+)[\"\'] ]]; then
        COMMIT_MSG="${BASH_REMATCH[1]}"
    fi

    if [ -n "$COMMIT_MSG" ]; then
        COMMIT_MSG_LOWER=$(echo "$COMMIT_MSG" | tr '[:upper:]' '[:lower:]')

        FORBIDDEN_PATTERNS=(
            "co-authored-by.*claude"
            "co-authored-by.*anthropic"
            "co-authored-by.*openai"
            "co-authored-by.*gpt"
            "co-authored-by.*ai"
            "co-authored-by.*copilot"
            "co-authored-by.*gemini"
            "co-authored-by.*llm"
            "generated.*with.*claude"
            "generated.*by.*claude"
            "generated.*with.*ai"
            "generated.*by.*ai"
            "generated.*with.*gpt"
            "generated.*by.*gpt"
            "ai.assisted"
            "ai-assisted"
            "🤖"
            "claude code"
            "claude-code"
            "anthropic"
            "openai"
            "chatgpt"
            "copilot"
        )

        for pattern in "${FORBIDDEN_PATTERNS[@]}"; do
            if echo "$COMMIT_MSG_LOWER" | grep -qiE "$pattern"; then
                echo "═══════════════════════════════════════════════" >&2
                echo "  ❌ COMMIT BLOCKED - AI reference detected" >&2
                echo "═══════════════════════════════════════════════" >&2
                echo "" >&2
                echo "  Forbidden pattern: $pattern" >&2
                echo "  Remove AI references from the commit message." >&2
                echo "" >&2
                echo "═══════════════════════════════════════════════" >&2
                exit 2
            fi
        done
    fi

    # === 3. Scan staged files for secrets (reads staged blobs, not working tree) ===
    ISSUES_FOUND=0

    # Inline lightweight secret scan (avoid calling security.sh subprocess)
    # Use process substitution to avoid subshell (preserves ISSUES_FOUND)
    while IFS= read -r -d '' f; do
        [ -z "$f" ] && continue

        # Skip hook/tooling scripts (contain regex patterns that match themselves)
        # Skip agents (contain security detection patterns), templates (.tpl),
        # lifecycle hooks (contain token variable references), and test fixtures
        case "$f" in
            */.claude/scripts/*|*/.claude/agents/*|*/.claude/commands/*) continue ;;
            */.githooks/*|*.bats|*.tpl) continue ;;
            */hooks/lifecycle/*) continue ;;
        esac

        # Read staged blob content (not working tree)
        STAGED_CONTENT=$(git show ":$f" 2>/dev/null) || continue

        # Skip binary files (check staged content)
        if printf '%s' "$STAGED_CONTENT" | file -- - 2>/dev/null | grep -q "binary"; then
            continue
        fi

        # Pattern-based secret detection on staged content
        if printf '%s' "$STAGED_CONTENT" | grep -iEq \
            'password\s*=\s*["\047][^"\047]+|api[_-]?key\s*=\s*["\047][^"\047]+|secret[_-]?key\s*=\s*["\047][^"\047]+|aws[_-]?access[_-]?key|BEGIN RSA PRIVATE KEY|BEGIN OPENSSH PRIVATE KEY|ghp_[a-zA-Z0-9]{36}|gho_[a-zA-Z0-9]{36}|github_pat_[a-zA-Z0-9_]+|sk-[a-zA-Z0-9]{48}|AKIA[0-9A-Z]{16}'; then
            echo "⚠️  Potential secret in: $f" >&2
            ISSUES_FOUND=1
        fi

        # detect-secrets (if available, more thorough)
        if [ $ISSUES_FOUND -eq 0 ] && command -v detect-secrets &>/dev/null; then
            if printf '%s' "$STAGED_CONTENT" | detect-secrets scan 2>/dev/null | python3 -c 'import sys,json; d=json.load(sys.stdin); exit(0 if any(d.get("results",{}).values()) else 1)'; then
                echo "⚠️  detect-secrets found issue in: $f" >&2
                ISSUES_FOUND=1
            fi
        fi
    done < <(git diff --cached --name-only -z 2>/dev/null)

    if [ $ISSUES_FOUND -eq 1 ]; then
        echo "═══════════════════════════════════════════════" >&2
        echo "  ⚠️  COMMIT BLOCKED - Secrets detected" >&2
        echo "═══════════════════════════════════════════════" >&2
        echo "  Remove secrets before committing." >&2
        echo "═══════════════════════════════════════════════" >&2
        exit 2
    fi
fi

exit 0
