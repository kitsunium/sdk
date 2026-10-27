#!/usr/bin/env bash
# scripts/ci/test-binaries.sh OUTDIR [MODULE...] — one `go test -c` binary for
# EVERY package with tests, in every module of the census (or the modules
# named), for the GOOS/GOARCH of the environment. Beside them, OUTDIR/manifest
# holds one line per binary: its file name, then the package's directory
# relative to the repository root — scripts/ci/run-test-binaries.sh runs each
# binary from that directory, as `go test` would.
#
# Why. The lanes that execute on a guest with no Go toolchain (the three BSDs,
# illumos, Solaris) cross-compile here and run there. They used to compile a
# hand-written list of "platform-sensitive" packages, so every other package —
# 300 of 333 — never ran on those kernels at all; the list is gone, and a
# package is executed on every kernel the moment it has a _test.go.
#
# `go test -c -o DIR/` cannot do it in one call: two packages of one module
# share a name (core/…/tee and service/…/tee), and the go command refuses to
# write both into one directory. So each package is its own invocation, run in
# parallel (TEST_BINARIES_JOBS, default the CPU count) over one shared build
# cache. `-s -w` drops the symbol table and DWARF, which a test run never
# reads (runtime.Caller and stack traces use pclntab, which stays): it halves
# what is synchronised into the guest.
#
# Exit 1, naming each package, when one does not compile — a package that
# cannot be built for a GOOS is a failure of that GOOS, never a skip.

set -euo pipefail

if [ "$#" -lt 1 ]; then
  echo "usage: test-binaries.sh OUTDIR [MODULE...]" >&2
  exit 2
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
mkdir -p "$1"
out="$(cd "$1" && pwd)"
shift

if [ "$#" -gt 0 ]; then
  modules="$*"
else
  modules="$(bash "$root/scripts/ci/go-modules.sh")"
fi

jobs="${TEST_BINARIES_JOBS:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 2)}"

# One package directory per line, relative to the root: what this GOOS builds
# a test binary for (go list applies the build constraints of GOOS/GOARCH).
pkgs="$out/.packages"
: >"$pkgs"
for mod in $modules; do
  (cd "$root/$mod" && go list -f '{{if or .TestGoFiles .XTestGoFiles}}{{.Dir}}{{end}}' ./...) |
    sed -e "s|^$root/||" -e "s|^$root\$|.|" >>"$pkgs"
done
if [ ! -s "$pkgs" ]; then
  echo "test-binaries.sh: no package with tests for $(go env GOOS)/$(go env GOARCH) — refusing an empty set" >&2
  exit 1
fi

# name <dir> — the binary's file name: the directory with / spelled __.
name() {
  if [ "$1" = . ]; then echo "_root.test"; else echo "${1//\//__}.test"; fi
}

compile() {
  local dir="$1" bin
  bin="$(name "$dir")"
  rm -f "$out/.fail.$bin"
  if ! (cd "$root/$dir" && go test -c -ldflags='-s -w' -o "$out/$bin" .) >"$out/.log.$bin" 2>&1; then
    mv "$out/.log.$bin" "$out/.fail.$bin"
    return 0
  fi
  rm -f "$out/.log.$bin"
}
export -f compile name
export out root

xargs -P "$jobs" -I{} bash -c 'compile "$1"' _ {} <"$pkgs"

failed=0
for f in "$out"/.fail.*; do
  [ -e "$f" ] || continue
  failed=1
  echo "::error::$(basename "$f" | sed 's/^\.fail\.//') does not compile for $(go env GOOS)/$(go env GOARCH)"
  cat "$f"
done
[ "$failed" -eq 0 ] || exit 1

# parts <dir> — the -test.run regular expressions a suite is cut by, one run
# of its binary each, plus a last run of every test none of them matches
# (-test.skip): a test of any name, one added later included, runs in exactly
# one run. Nothing for a suite run whole, which is every suite but those
# below. Each run has its own -test.timeout in run-test-binaries.sh, which
# stays the alarm it is: a suite is cut, never given a longer cap.
#
# internal/service/app/consensus: some 300 cluster and disk scenarios that
# wait rather than compute (its CLAUDE.md, §Lanes; 24 shards under Bazel).
# Whole, it took 272 to 301 s on NetBSD 10.1 and timed out there at the cap
# in 19 runs of 55 (#701); it took 268 s on OpenBSD 7.9. Measured with
# -test.v on NetBSD, ^TestA held 163 s of 298, the rest 135.
parts() {
  case "$1" in
    internal/service/app/consensus) echo '^TestA' ;;
  esac
}

# The manifest, in the census order. A package whose test files all carry a
# constraint this GOOS does not meet is not listed by go list above, so every
# directory here has a binary; a missing one is a defect of this script. A
# line is the binary and its directory, then, for a suite cut by parts, the
# run as k/n, its -test.run and its -test.skip, "-" standing for none.
: >"$out/manifest"
while IFS= read -r dir; do
  bin="$(name "$dir")"
  if [ ! -f "$out/$bin" ]; then
    echo "::error::go test -c wrote no binary for $dir" >&2
    exit 1
  fi
  read -r -a res <<<"$(parts "$dir")"
  if [ "${#res[@]}" -eq 0 ]; then
    printf '%s %s\n' "$bin" "$dir" >>"$out/manifest"
    continue
  fi
  n=$((${#res[@]} + 1))
  before=""
  for k in "${!res[@]}"; do
    printf '%s %s %s %s %s\n' "$bin" "$dir" "$((k + 1))/$n" "${res[$k]}" "${before:--}" >>"$out/manifest"
    before="${before:+$before|}${res[$k]}"
  done
  printf '%s %s %s %s %s\n' "$bin" "$dir" "$n/$n" - "$before" >>"$out/manifest"
done <"$pkgs"
rm -f "$pkgs"

echo "test-binaries.sh: $(cut -d' ' -f1 "$out/manifest" | sort -u | wc -l | tr -d ' ') test binaries, $(wc -l <"$out/manifest" | tr -d ' ') runs, for $(go env GOOS)/$(go env GOARCH), $(du -sh "$out" | cut -f1)"
