#!/usr/bin/env bash
# scripts/ci/go-modules.sh — every Go module in this repository: one directory
# per line, `.` for the root, sorted. The lanes that loop over modules read it
# instead of carrying a list of their own (ADR 0137).
#
# Why a census and not a list. `./...` stops at the first nested go.mod, so a
# lane that loops over modules only reaches the ones somebody wrote down — and
# three copies had drifted apart: cross-build looped over six modules, the
# 32-bit test lane over four, scripts/cross-platform-audit.sh over five, and
# none of them over tools/genindex or tools/sdkguard, whose 477 tests nothing
# ever ran on 386 (#242). ktn-linter and sdkguard walk PATHS and reached tools/;
# go build, go vet and go test walk MODULES and did not, which is why the tree
# looked covered. A module added tomorrow is in every lane the moment git
# tracks its go.mod, and a lane that must skip one says so, by name, next to
# the loop.
#
# Derived from `git ls-files`, so an untracked scratch module is not reported:
# CI checks out exactly what git tracks, and the census answers for that tree.
# A go.mod under testdata/ is a fixture the go command itself never treats as a
# module of this repository, so it is left out.
#
# Exit 1, with the reason on stderr, when git cannot answer or answers nothing:
# a lane looping over an empty census would pass having built nothing, which is
# the shape of the defect this exists to close.

set -euo pipefail

cd "$(dirname "$0")/../.."

tracked=""
rc=0
tracked="$(git ls-files -- 'go.mod' '*/go.mod')" || rc=$?
if [ "$rc" -ne 0 ]; then
  echo "go-modules.sh: git ls-files failed (exit $rc) — refusing to answer with no census" >&2
  exit 1
fi

modules=()
while IFS= read -r gomod; do
  [ -z "$gomod" ] && continue
  case "/$gomod" in
    */testdata/*) continue ;;
  esac
  dir="$(dirname "$gomod")"
  modules+=("$dir")
done <<<"$tracked"

if [ "${#modules[@]}" -eq 0 ]; then
  echo "go-modules.sh: git tracks no go.mod — refusing to answer with an empty census" >&2
  exit 1
fi

printf '%s\n' "${modules[@]}" | LC_ALL=C sort
