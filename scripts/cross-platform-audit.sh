#!/usr/bin/env bash
# ============================================================================
# cross-platform-audit.sh — answers, per Go package, "does it build on every
# target platform we support?" YES/NO, and prints a matrix.
#
# It cross-compiles every package in every SDK module against the full GOOS
# matrix (no cgo, pure build — the proc syscalls are stdlib `syscall`, so a
# clean `go build` is a faithful portability signal). A package that fails on a
# platform either (a) references a syscall/constant absent on that GOOS, or
# (b) lacks a build-tagged fallback. Both are gaps to close so the package
# behaves uniformly everywhere.
#
# Usage:  bash scripts/cross-platform-audit.sh [--quiet]
# Output: a per-package × per-platform table; exit 1 if any cell is FAIL.
# ============================================================================

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# GOOS/GOARCH targets. linux+darwin are primary; the BSDs + windows are the
# portability frontier the SDK must not silently drop.
PLATFORMS=(
  linux/amd64 linux/arm64
  darwin/arm64
  windows/amd64
  freebsd/amd64 openbsd/amd64 netbsd/amd64 dragonfly/amd64
)

# module dir -> import-path prefix is discovered via `go list`; each module is
# built with GOWORK=off so its own go.mod + replace directives resolve.
MODULES=(internal/kernel internal/core internal/service pkg .)

QUIET=0
[[ "${1:-}" == "--quiet" ]] && QUIET=1

# results[pkg|plat] = OK|FAIL
declare -A results
declare -A reason
ALL_PKGS=()

note() { [[ $QUIET -eq 0 ]] && echo "$@" >&2 || true; }

for mod in "${MODULES[@]}"; do
  note "→ listing packages in $mod"
  mapfile -t pkgs < <(cd "$mod" && GOWORK=off go list ./... 2>/dev/null)
  for pkg in "${pkgs[@]}"; do
    [[ -z "$pkg" ]] && continue
    ALL_PKGS+=("$pkg")
    for plat in "${PLATFORMS[@]}"; do
      os="${plat%/*}"; arch="${plat#*/}"
      if out=$(cd "$mod" && GOWORK=off GOOS="$os" GOARCH="$arch" go build "$pkg" 2>&1); then
        results["$pkg|$plat"]=OK
      else
        results["$pkg|$plat"]=FAIL
        reason["$pkg|$plat"]=$(echo "$out" | grep -vE '^Go build:|^#' | head -1)
      fi
    done
  done
done

# ---- render the matrix -----------------------------------------------------
printf '\n# Cross-platform build matrix\n\n'
printf '| package |'
for plat in "${PLATFORMS[@]}"; do printf ' %s |' "${plat#*/} ${plat%/*}"; done
printf '\n|---|'
for _ in "${PLATFORMS[@]}"; do printf '---|'; done
printf '\n'

fail_total=0
declare -A reasons_seen
for pkg in "${ALL_PKGS[@]}"; do
  short="${pkg#github.com/kitsunium/sdk/}"
  printf '| `%s` |' "$short"
  for plat in "${PLATFORMS[@]}"; do
    cell="${results["$pkg|$plat"]:-?}"
    if [[ "$cell" == OK ]]; then printf ' ✅ |'; else printf ' ❌ |'; fail_total=$((fail_total+1)); reasons_seen["$short @ $plat"]="${reason["$pkg|$plat"]:-}"; fi
  done
  printf '\n'
done

# ---- failure reasons -------------------------------------------------------
if [[ $fail_total -gt 0 ]]; then
  printf '\n## Failures (%d cells)\n\n' "$fail_total"
  for key in "${!reasons_seen[@]}"; do
    printf -- '- **%s** — %s\n' "$key" "${reasons_seen[$key]}"
  done | sort
fi

printf '\n_%d packages × %d platforms; %d failing cells._\n' "${#ALL_PKGS[@]}" "${#PLATFORMS[@]}" "$fail_total"

[[ $fail_total -eq 0 ]]
