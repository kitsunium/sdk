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

# The manifest, in the census order. A package whose test files all carry a
# constraint this GOOS does not meet is not listed by go list above, so every
# directory here has a binary; a missing one is a defect of this script.
: >"$out/manifest"
while IFS= read -r dir; do
  bin="$(name "$dir")"
  if [ ! -f "$out/$bin" ]; then
    echo "::error::go test -c wrote no binary for $dir" >&2
    exit 1
  fi
  printf '%s %s\n' "$bin" "$dir" >>"$out/manifest"
done <"$pkgs"
rm -f "$pkgs"

echo "test-binaries.sh: $(wc -l <"$out/manifest" | tr -d ' ') test binaries for $(go env GOOS)/$(go env GOARCH), $(du -sh "$out" | cut -f1)"
