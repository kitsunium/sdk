#!/usr/bin/env bash
# Consumer-side patch chained after /etc/devcontainer-hooks/lifecycle/postStart.sh.
# Idempotent. Remove when both fixes ship upstream in kodflow/devcontainer-template.
#
# Tracks two regressions not yet in the GHCR base image:
#
#   1. `rtk init --auto-patch` re-injects a raw `rtk hook claude` PreToolUse
#      entry on every start; our settings.json source-of-truth wires the
#      wrapper rtk-hook-claude.sh, so leaving both makes rtk run twice per
#      Bash call and breaks the wrapper's fail-open guarantee.
#
#   2. Older versions of the Go feature's install.sh did not always write
#      /etc/mcp/features/go.mcp.json. Without it, step_mcp_configuration
#      silently leaves ktn-linter out of /workspace/mcp.json on rebuild.

set -uo pipefail

log() { printf '[fixup] %s\n' "$*"; }

# Remove single-hook PreToolUse Bash entries whose only command is the raw
# `rtk hook claude`. The wrapper rtk-hook-claude.sh, written by settings.json
# source-of-truth, stays untouched.
fixup_rtk_dedupe() {
    local settings="$HOME/.claude/settings.json"
    [ -f "$settings" ] || return 0
    command -v jq >/dev/null 2>&1 || return 0

    local tmp
    tmp=$(mktemp) || return 0
    if jq '
        if (.hooks.PreToolUse | type) == "array" then
            .hooks.PreToolUse |= map(
                select(
                    ((.matcher // "") != "Bash")
                    or
                    ((.hooks // []) | length != 1)
                    or
                    ((.hooks // [])[0].command != "rtk hook claude")
                )
            )
        else . end
    ' "$settings" > "$tmp" 2>/dev/null && jq empty "$tmp" 2>/dev/null; then
        if ! cmp -s "$settings" "$tmp"; then
            mv "$tmp" "$settings"
            log "removed duplicate raw 'rtk hook claude' entry (wrapper preserved)"
        else
            rm -f "$tmp"
        fi
    else
        rm -f "$tmp"
        log "WARN: rtk dedupe pass failed (jq error); settings.json untouched"
    fi
}

# Synthesize /etc/mcp/features/go.mcp.json when ktn-linter is on PATH but the
# fragment is absent. The content is byte-equivalent to what the Go feature's
# install.sh writes when it runs cleanly.
fixup_go_mcp_fragment() {
    local fragment="/etc/mcp/features/go.mcp.json"
    [ -f "$fragment" ] && return 0
    command -v ktn-linter >/dev/null 2>&1 || return 0

    if ! mkdir -p /etc/mcp/features 2>/dev/null; then
        log "WARN: cannot create /etc/mcp/features (permission denied)"
        return 0
    fi

    local tmp
    tmp=$(mktemp) || return 0
    cat > "$tmp" <<'EOF'
{
  "servers": {
    "ktn-linter": {
      "command": "ktn-linter",
      "args": ["serve"],
      "requires_binary": "ktn-linter"
    }
  }
}
EOF
    if mv "$tmp" "$fragment" 2>/dev/null; then
        log "synthesized Go MCP fragment at $fragment"
    else
        rm -f "$tmp"
        log "WARN: cannot write $fragment (permission denied)"
    fi
}

# Patch /workspace/mcp.json directly: step_mcp_configuration already ran
# before this fixup, so a freshly-synthesized fragment won't be picked up
# this cycle. Idempotent — bails when ktn-linter is already present.
fixup_workspace_mcp_json() {
    local mcp="${WORKSPACE_FOLDER:-/workspace}/mcp.json"
    [ -f "$mcp" ] || return 0
    [ -f "/etc/mcp/features/go.mcp.json" ] || return 0
    command -v jq >/dev/null 2>&1 || return 0
    command -v ktn-linter >/dev/null 2>&1 || return 0

    if jq -e '.mcpServers["ktn-linter"]' "$mcp" >/dev/null 2>&1; then
        return 0
    fi

    local tmp
    tmp=$(mktemp) || return 0
    if jq '.mcpServers["ktn-linter"] = {"command":"ktn-linter","args":["serve"]}' \
            "$mcp" > "$tmp" 2>/dev/null && jq empty "$tmp" 2>/dev/null; then
        mv "$tmp" "$mcp"
        chmod 600 "$mcp" 2>/dev/null || true
        log "added ktn-linter MCP server to $mcp"
    else
        rm -f "$tmp"
        log "WARN: failed to add ktn-linter to $mcp"
    fi
}

fixup_rtk_dedupe
fixup_go_mcp_fragment
fixup_workspace_mcp_json

exit 0
