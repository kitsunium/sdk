#!/usr/bin/env bash
# usage: .tmp/t.sh <pkgdir> [pkgdir...]  — runs go test in the owning module
set -u
R=/home/florent/kepler/repositories/.worktrees/sdk-jaimerais-que-tu-mexplique-dans-5b7bcb73
rc=0
for p in "$@"; do
  case "$p" in
    internal/core/*|internal/core) m=internal/core;;
    internal/kernel/*|internal/kernel) m=internal/kernel;;
    internal/service/*|internal/service) m=internal/service;;
    pkg/*) m=pkg;;
    *) m=.;;
  esac
  sub=${p#"$m"/}; [ "$sub" = "$p" ] && sub=.
  out=$(cd "$R/$m" && GOWORK=off go test "./$sub/..." 2>&1)
  echo "$out" | grep -Ev '^(ok|\?)' | head -25
  echo "$out" | grep -E '^(ok|FAIL|---)' | head -5
  echo "$out" | grep -q '^FAIL' && rc=1
done
exit $rc
