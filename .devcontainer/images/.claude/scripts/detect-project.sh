#!/bin/bash
# ============================================================================
# detect-project.sh - Detect project languages, build system, tools in 1 call
# Usage: detect-project.sh [project_dir]
# Exit 0 = always (fail-open)
#
# Replaces 19 sequential marker checks with 1 script call.
# Used by: /lint, /init, /warmup, /docs, /do, /audit
# ============================================================================

set +e  # Fail-open

PROJECT_DIR="${1:-${CLAUDE_PROJECT_DIR:-/workspace}}"

# Source shared utilities
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ -f "$SCRIPT_DIR/common.sh" ] && . "$SCRIPT_DIR/common.sh"

# Guard: find_project_root may not exist if common.sh missing
if type find_project_root &>/dev/null; then
    PROJECT_ROOT=$(find_project_root "$PROJECT_DIR" "$PROJECT_DIR")
else
    PROJECT_ROOT="$PROJECT_DIR"
fi

# Require jq for JSON output
if ! command -v jq &>/dev/null; then
    echo '{"error":"jq not installed"}' >&2
    exit 0
fi

# --- Language detection via markers ---
LANGUAGES="[]"
add_lang() {
    local name="$1" marker="$2" primary="$3"
    LANGUAGES=$(echo "$LANGUAGES" | jq --arg n "$name" --arg m "$marker" --argjson p "$primary" \
        '. + [{"name": $n, "marker": $m, "primary": $p}]')
}

FIRST=true
check_marker() {
    local name="$1" marker="$2"
    if [ -f "$PROJECT_ROOT/$marker" ]; then
        if [ "$FIRST" = true ]; then
            add_lang "$name" "$marker" true
            FIRST=false
        else
            add_lang "$name" "$marker" false
        fi
    fi
}

check_marker "go" "go.mod"
check_marker "rust" "Cargo.toml"
check_marker "nodejs" "package.json"
check_marker "python" "pyproject.toml"
[ -f "$PROJECT_ROOT/setup.py" ] && ! echo "$LANGUAGES" | jq -e '.[] | select(.name=="python")' &>/dev/null && add_lang "python" "setup.py" "$([[ "$FIRST" == true ]] && echo true || echo false)"
check_marker "java" "pom.xml"
[ -f "$PROJECT_ROOT/build.gradle" ] && ! echo "$LANGUAGES" | jq -e '.[] | select(.name=="java")' &>/dev/null && add_lang "java" "build.gradle" "$([[ "$FIRST" == true ]] && echo true || echo false)"
[ -f "$PROJECT_ROOT/build.gradle.kts" ] && ! echo "$LANGUAGES" | jq -e '.[] | select(.name=="kotlin")' &>/dev/null && add_lang "kotlin" "build.gradle.kts" "$([[ "$FIRST" == true ]] && echo true || echo false)"
# csproj uses glob - check with find instead of -f
if find "$PROJECT_ROOT" -maxdepth 1 -name "*.csproj" -print -quit 2>/dev/null | grep -q .; then
    if [ "$FIRST" = true ]; then add_lang "csharp" "*.csproj" true; FIRST=false
    else add_lang "csharp" "*.csproj" false; fi
fi
check_marker "ruby" "Gemfile"
check_marker "php" "composer.json"
check_marker "elixir" "mix.exs"
check_marker "dart" "pubspec.yaml"
check_marker "scala" "build.sbt"
check_marker "swift" "Package.swift"
check_marker "fortran" "fpm.toml"
check_marker "ada" "alire.toml"
check_marker "c_cpp" "CMakeLists.txt"

# Sub-check: TypeScript detection
if echo "$LANGUAGES" | jq -e '.[] | select(.name=="nodejs")' &>/dev/null; then
    if [ -f "$PROJECT_ROOT/tsconfig.json" ]; then
        add_lang "typescript" "tsconfig.json" false
    fi
fi

# Fallback: scan extensions if no markers found
if [ "$(echo "$LANGUAGES" | jq length)" = "0" ]; then
    for ext_lang in "lua:.lua" "perl:.pl" "r:.R" "pascal:.pas" "vbnet:.vb" "cobol:.cob" "fortran:.f90" "ada:.adb" "scala:.scala" "kotlin:.kt"; do
        lang="${ext_lang%%:*}"
        ext="${ext_lang#*:}"
        if find "$PROJECT_ROOT" -maxdepth 3 -name "*${ext}" -print -quit 2>/dev/null | grep -q .; then
            add_lang "$lang" "*${ext}" "$([[ "$FIRST" == true ]] && echo true || echo false)"
            FIRST=false
        fi
    done
fi

# --- Build system ---
HAS_MAKEFILE=false
TARGETS="[]"
if [ -f "$PROJECT_ROOT/Makefile" ]; then
    HAS_MAKEFILE=true
    TARGETS=$(grep -oE '^[a-zA-Z_-]+:' "$PROJECT_ROOT/Makefile" 2>/dev/null | sed 's/://' | jq -R . | jq -s . 2>/dev/null || echo "[]")
fi

# --- Tool availability ---
tools_json() {
    local result="{}"
    for tool in ktn-linter eslint ruff golangci-lint cargo-clippy rubocop phpstan ktlint swiftlint luacheck shellcheck hadolint clang-tidy mypy tsc prettier; do
        local safe_name
        safe_name=$(echo "$tool" | tr '-' '_')
        if command -v "$tool" &>/dev/null; then
            result=$(echo "$result" | jq --arg k "$safe_name" '. + {($k): true}')
        else
            result=$(echo "$result" | jq --arg k "$safe_name" '. + {($k): false}')
        fi
    done
    echo "$result"
}

TOOLS=$(tools_json)

# --- Project type ---
PROJECT_TYPE="code"
if [ -f "$PROJECT_ROOT/main.tf" ] || [ -f "$PROJECT_ROOT/terragrunt.hcl" ]; then
    PROJECT_TYPE="iac"
elif [ "$(echo "$LANGUAGES" | jq length)" = "0" ]; then
    PROJECT_TYPE="none"
fi

# --- Output JSON ---
jq -n \
    --argjson languages "$LANGUAGES" \
    --argjson has_makefile "$HAS_MAKEFILE" \
    --argjson targets "$TARGETS" \
    --argjson tools "$TOOLS" \
    --arg project_root "$PROJECT_ROOT" \
    --arg project_type "$PROJECT_TYPE" \
    '{
        languages: $languages,
        build_system: {makefile: $has_makefile, targets: $targets},
        tools: $tools,
        project_root: $project_root,
        project_type: $project_type
    }'
