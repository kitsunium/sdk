#!/bin/bash
# ============================================================================
# common.sh - Shared utilities for hook scripts
# ============================================================================
# Sourced by format.sh, lint.sh, typecheck.sh, test.sh to avoid duplication.
# ============================================================================

# Hook profile gating — controls which hooks execute based on HOOK_PROFILE env var.
# Levels: minimal (critical only), standard (+ quality), full (all, default)
#
# Usage: check_hook_profile "standard" || exit 0
#   critical  = always runs (git-guard, security)
#   standard  = runs in standard + full (lint, format, quality)
#   full      = runs only in full mode (observe, feature-update, logging)
check_hook_profile() {
    local required_level="$1"
    local current="${HOOK_PROFILE:-full}"

    case "$current" in
        minimal)
            if [ "$required_level" = "critical" ]; then
                return 0
            fi
            return 1 ;;
        standard)
            if [ "$required_level" = "critical" ] || [ "$required_level" = "standard" ]; then
                return 0
            fi
            return 1 ;;
        full|*)
            return 0 ;;
    esac
}

# Find project root by walking up from a given directory
# Checks for common build/config files that indicate a project root.
# Usage: PROJECT_ROOT=$(find_project_root "$DIR")
find_project_root() {
    local current="$1"
    local fallback="${2:-$current}"
    while [ "$current" != "/" ]; do
        if [ -f "$current/Makefile" ] || \
           [ -f "$current/package.json" ] || \
           [ -f "$current/pyproject.toml" ] || \
           [ -f "$current/go.mod" ] || \
           [ -f "$current/Cargo.toml" ] || \
           [ -f "$current/pom.xml" ] || \
           [ -f "$current/build.gradle" ] || \
           [ -f "$current/build.gradle.kts" ] || \
           [ -f "$current/build.sbt" ] || \
           [ -f "$current/mix.exs" ] || \
           [ -f "$current/pubspec.yaml" ] || \
           [ -f "$current/CMakeLists.txt" ] || \
           [ -f "$current/Package.swift" ] || \
           [ -f "$current/composer.json" ] || \
           [ -f "$current/Gemfile" ] || \
           [ -f "$current/fpm.toml" ] || \
           [ -f "$current/alire.toml" ]; then
            echo "$current"
            return
        fi
        current=$(dirname "$current")
    done
    echo "$fallback"
}

# Check if Makefile has a specific target
# Usage: has_makefile_target "lint" "$PROJECT_ROOT"
has_makefile_target() {
    local target="$1"
    local root="${2:-.}"
    if [ -f "$root/Makefile" ]; then
        grep -qE "^${target}:" "$root/Makefile" 2>/dev/null
        return $?
    fi
    return 1
}

# Check if Makefile supports FILE= parameter
# Usage: makefile_supports_file "$PROJECT_ROOT"
makefile_supports_file() {
    local root="${1:-.}"
    grep -qE "FILE\s*[:?]?=" "$root/Makefile" 2>/dev/null
}

# Run a Makefile target with optional FILE parameter
# Usage: run_makefile_target "fmt" "$FILE" "$PROJECT_ROOT"
run_makefile_target() {
    local target="$1"
    local file="$2"
    local root="${3:-.}"
    cd "$root" || exit 0
    if makefile_supports_file "$root"; then
        make "$target" FILE="$file" 2>/dev/null || true
    else
        make "$target" 2>/dev/null || true
    fi
}

# Get files changed on current branch vs base + working tree
# Returns one path per line (relative to repo root), deduplicated.
# On main/base branch: only working tree + index (no branch diff).
# Usage: CHANGED=$(get_branch_changed_files [base_branch] [project_dir])
get_branch_changed_files() {
    local base="${1:-main}"
    local project_dir="${2:-${CLAUDE_PROJECT_DIR:-/workspace}}"
    (
        cd "$project_dir" 2>/dev/null || return
        local current_branch
        current_branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
        {
            # Branch diff only if not on the base branch itself
            if [ -n "$current_branch" ] && [ "$current_branch" != "$base" ]; then
                git diff --name-only "$base"...HEAD 2>/dev/null
            fi
            # Always include working tree + index changes
            git diff --name-only 2>/dev/null
            git diff --cached --name-only 2>/dev/null
        } | sort -u
    )
}
