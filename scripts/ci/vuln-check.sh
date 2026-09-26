#!/usr/bin/env bash
# scripts/ci/vuln-check.sh — govulncheck, in source mode, over every module of
# the census (scripts/ci/go-modules.sh), one module at a time (ADR 0136).
#
# Why it exists. ADR 0004 retired the four govulncheck jobs of the old pipeline
# in favour of Bazel as the single build system, and `bazel test` carries no
# vulnerability database, so from then on nothing scanned the runtime modules;
# the one scan left, over the gomarkdoc BINARY, never covered SDK code and was
# removed for a valid reason (its findings sat in paths gomarkdoc never
# reaches). Dependabot alerts on the manifest and cannot tell a vulnerable
# symbol the SDK calls from one it never reaches; govulncheck walks the call
# graph and can. The last scan anybody ran was by hand (#210).
#
# The verdict per module, from govulncheck's exit status:
#   0  no vulnerability the module's code reaches — including when the module
#      only IMPORTS or REQUIRES a vulnerable package or module it never calls,
#      which govulncheck reports and does not fail on, and neither does this;
#   3  a vulnerable symbol is reachable — FAILS, and this is the gate;
#   anything else  govulncheck did not complete (no network to vuln.go.dev, a
#      module that does not load) — FAILS too, under its own name, because a
#      scan that did not run is not a clean scan.
#
# Every module is scanned even after one fails, so one run reports them all.
# The standard library is part of the scan: its version is the toolchain's, and
# a reachable finding there is settled by moving the `go` line and MODULE.bazel's
# go_sdk together, which is also what moves every consumer's floor.
#
# Usage: vuln-check.sh [module…]   (default: the whole census)
# GOVULNCHECK names the binary (default: govulncheck on PATH). When
# GOVULNCHECK_VERSION is set — `make vuln-check` sets it from the Makefile's pin
# — the scanner must report exactly that version: a verdict from a different
# scanner is a different verdict, and a lane that follows whatever is installed
# changes its answer with no commit of this repository moving.

set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
scanner="${GOVULNCHECK:-govulncheck}"

if ! command -v "$scanner" >/dev/null 2>&1; then
  echo "vuln-check: '$scanner' is not on PATH — install the pinned build with: make vuln-install" >&2
  exit 127
fi

if [ -n "${GOVULNCHECK_VERSION:-}" ]; then
  reported=""
  reported="$("$scanner" -version 2>&1)" || {
    echo "vuln-check: '$scanner -version' failed:" >&2
    printf '%s\n' "$reported" >&2
    exit 1
  }
  case "$reported" in
    *"govulncheck@${GOVULNCHECK_VERSION}"*) ;;
    *)
      echo "vuln-check: expected govulncheck@${GOVULNCHECK_VERSION}, '$scanner -version' reported:" >&2
      printf '%s\n' "$reported" >&2
      echo "vuln-check: install the pinned build with: make vuln-install" >&2
      exit 1
      ;;
  esac
fi

if [ "$#" -gt 0 ]; then
  modules=("$@")
else
  census=""
  census="$(bash "$here/go-modules.sh")"
  modules=()
  while IFS= read -r mod; do
    [ -n "$mod" ] && modules+=("$mod")
  done <<<"$census"
fi

vulnerable=()
incomplete=()
for mod in "${modules[@]}"; do
  echo "::group::govulncheck $mod"
  rc=0
  (cd "$root/$mod" && GOWORK=off "$scanner" ./...) || rc=$?
  echo "::endgroup::"
  case "$rc" in
    0) echo "vuln-check: $mod — no reachable vulnerability" ;;
    3) vulnerable+=("$mod") ;;
    *) incomplete+=("$mod (exit $rc)") ;;
  esac
done

fail=0
if [ "${#vulnerable[@]}" -gt 0 ]; then
  fail=1
  for mod in "${vulnerable[@]}"; do
    echo "::error::vuln-check: $mod reaches a known-vulnerable symbol — the scan above names it, the fixed version and the call path"
  done
fi
if [ "${#incomplete[@]}" -gt 0 ]; then
  fail=1
  for mod in "${incomplete[@]}"; do
    echo "::error::vuln-check: govulncheck did not complete for $mod — a scan that did not run is not a clean scan"
  done
fi
if [ "$fail" -ne 0 ]; then
  exit 1
fi
echo "vuln-check: ${#modules[@]} module(s) scanned, no reachable vulnerability"
