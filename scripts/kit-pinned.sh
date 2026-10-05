#!/usr/bin/env bash
# kit-pinned.sh — sourced, never run: `kit_pinned NAME` refuses, naming NAME,
# unless `kit` on PATH is the version design/sdk.yaml pins
# (project.kit.version), and sets KIT_PINNED to it. It is what scripts/regen.sh
# checks inline, shared by scripts/from-zero.sh and scripts/kit-coverage.sh
# (ADR 0169). The caller is at the repository root.
#
# Runs under macOS's /bin/bash 3.2 and the BSD tools (scripts/CLAUDE.md).

kit_pinned() {
  local name="$1" want have
  want="$(awk '
    /^project:/ { p = 1; next }
    p && /^[^ #]/ { p = 0 }
    p && /^  kit:/ { k = 1; next }
    k && /^  [^ #]/ { k = 0 }
    k && /^    version:/ { print $2; exit }
  ' design/sdk.yaml)"
  if [ -z "$want" ]; then
    printf '%s: design/sdk.yaml pins no kit (project.kit.version)\n' "$name" >&2
    return 1
  fi
  if ! command -v kit >/dev/null 2>&1; then
    printf '%s: kit is not on PATH; install kit %s (kitsunium/platform) and retry\n' "$name" "$want" >&2
    return 1
  fi
  if ! have="$(kit version 2>/dev/null | awk 'NR == 1 { print $2 }')"; then
    printf '%s: kit version failed; cannot verify the pinned kit %s\n' "$name" "$want" >&2
    return 1
  fi
  if [ "$have" != "$want" ]; then
    printf '%s: kit on PATH is %s, design/sdk.yaml pins %s; install kit %s and retry\n' "$name" "${have:-unknown}" "$want" "$want" >&2
    return 1
  fi
  export KIT_PINNED="$want"
}
