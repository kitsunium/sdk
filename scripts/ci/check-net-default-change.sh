#!/usr/bin/env bash
# scripts/ci/check-net-default-change.sh — the gate of the `net:default-change`
# label (ADR 0188): a pull request that changes a default of the network
# engines says, in its body, how an operator gets the old behaviour back, and
# says it in the registry's words.
#
# The body must hold an `Override:` block: a line `Override:` alone, then one
# line per variable until a blank line, a fence or the end, each
# `KITSUNIUM_NET_ENGINE=<token>` or `KITSUNIUM_NET_DISABLE=<token>[,<token>…]`,
# each variable at most once. An ENGINE token is any capability of the
# registry; a DISABLE token is one of the block an operator may switch off.
# The registry is design/net.yaml's Capability constants, read here, so the
# gate and the parser (internal/service/net/server, held to the same file by
# TestCapabilityRegistry_matchesTheDesign) cannot disagree.
#
# It runs on the pull request and in the merge queue (.github/workflows/
# net-default-change.yml), never at the release: release-notes.sh only renders
# the block back (ADR 0187 §D10).
#
# Usage: check-net-default-change.sh <body-file> [<design-file>]
# Exit 0 when the block matches the registry, 1 when it is missing or does
# not, 64 on bad usage.

set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ]; then
  echo "usage: $0 <body-file> [<design-file>]" >&2
  exit 64
fi
body="$1"
design="${2:-$(cd "$(dirname "$0")/../.." && pwd)/design/net.yaml}"

if [ ! -r "$body" ]; then
  echo "net-default-change: $body is missing or unreadable" >&2
  exit 1
fi
if [ ! -r "$design" ]; then
  echo "net-default-change: $design is missing or unreadable" >&2
  exit 1
fi

# registry <design> — "<token> <switchable>" per Capability constant of the
# design, in its order: switchable when its block's doc names
# KITSUNIUM_NET_DISABLE.
registry() {
  awk '
    /^      - blockDoc: / { block = $0; next }
    /^      - / { block = "" }
    /^        type: / { type = $2; next }
    /^        value: / {
      if (type == "Capability" && block != "") {
        v = $0
        sub(/^        value: "\\"/, "", v)
        sub(/\\""$/, "", v)
        print v, (index(block, "KITSUNIUM_NET_DISABLE") > 0 ? "yes" : "no")
      }
      type = ""
    }
  ' "$1"
}

reg="$(registry "$design")"
if [ -z "$reg" ]; then
  echo "net-default-change: no Capability constant in $design — the registry moved, or the pattern drifted" >&2
  exit 1
fi

# The block: the lines after `Override:`, up to a blank line, a fence or the
# end. Carriage returns are dropped: a body edited in a browser has them.
block="$(tr -d '\r' <"$body" | awk '
  found && (/^[[:space:]]*$/ || /^[[:space:]]*```/) { exit }
  found { sub(/^[[:space:]]+/, ""); sub(/[[:space:]]+$/, ""); print; next }
  /^[[:space:]]*Override:[[:space:]]*$/ { found = 1 }
')"
if [ -z "$block" ]; then
  echo "net-default-change: the pull request is labelled net:default-change and its body holds no Override: block" >&2
  echo "net-default-change:   add a line 'Override:' followed by KITSUNIUM_NET_ENGINE=<token> and/or KITSUNIUM_NET_DISABLE=<token>,… — how an operator gets the old default back" >&2
  exit 1
fi

known() {
  awk -v t="$1" -v need="$2" '$1 == t && (need == "any" || $2 == "yes") { found = 1 } END { exit !found }' <<<"$reg"
}

fail=0 seen_engine=0 seen_disable=0
while IFS= read -r line; do
  name="${line%%=*}"
  value="${line#*=}"
  if [ "$name" = "$line" ] || [ -z "$value" ]; then
    echo "net-default-change: '$line' is no NAME=value line" >&2
    fail=1
    continue
  fi
  case "$name" in
    KITSUNIUM_NET_ENGINE)
      seen_engine=$((seen_engine + 1))
      if ! known "$value" any; then
        echo "net-default-change: KITSUNIUM_NET_ENGINE=$value names no capability of the registry ($(awk '{ printf "%s%s", sep, $1; sep = ", " }' <<<"$reg"))" >&2
        fail=1
      fi
      ;;
    KITSUNIUM_NET_DISABLE)
      seen_disable=$((seen_disable + 1))
      IFS=',' read -r -a tokens <<<"$value"
      for t in "${tokens[@]}"; do
        t="${t#"${t%%[![:space:]]*}"}"
        t="${t%"${t##*[![:space:]]}"}"
        [ -z "$t" ] && continue
        if ! known "$t" switchable; then
          echo "net-default-change: KITSUNIUM_NET_DISABLE token '$t' is no capability an operator may switch off ($(awk '$2 == "yes" { printf "%s%s", sep, $1; sep = ", " }' <<<"$reg"))" >&2
          fail=1
        fi
      done
      ;;
    *)
      echo "net-default-change: '$name' is not one of the operator's variables (KITSUNIUM_NET_ENGINE, KITSUNIUM_NET_DISABLE)" >&2
      fail=1
      ;;
  esac
done <<<"$block"

if [ "$seen_engine" -gt 1 ] || [ "$seen_disable" -gt 1 ]; then
  echo "net-default-change: each variable is given at most once in the Override: block" >&2
  fail=1
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "net-default-change: the Override: block matches the registry"
