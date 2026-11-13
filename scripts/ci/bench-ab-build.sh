#!/usr/bin/env bash
#
# bench-ab-build.sh TREE PKGDIR OUTBIN [OVERLAY_TREE] — compiles the test
# binary of the package PKGDIR (relative to the checked-out tree TREE) into
# OUTBIN: `GOWORK=off go test -c` in the package's module, the nearest go.mod
# above it. GO names the go command (default go).
#
# With OVERLAY_TREE — the head's tree, when TREE is the base's — the base is
# built with the head's benchmark files, so both binaries carry the same rows
# measured by the same code. The head's files may need what the base does not
# have, so:
#   all      every *_bench_test.go of the head's package copied over the base's,
#            when that compiles;
#   partial  otherwise one file at a time — those the base also has first,
#            then the head's new ones —, each kept when the base still
#            compiles with it, the base's own version left where it does not;
#   none     no head file compiles on the base: its own bench files.
# Every attempt that does not compile is undone before the next, its errors on
# stderr. With an overlay, prints `overlay=<step>` and one `overlay-file=<name>` a file kept, on
# stdout. Exit 1 when the base does not compile even alone, 2 on bad usage.

set -euo pipefail

die() {
  printf 'bench-ab-build: %s\n' "$*" >&2
  exit 2
}

[ $# -eq 3 ] || [ $# -eq 4 ] || die "usage: bench-ab-build.sh TREE PKGDIR OUTBIN [OVERLAY_TREE]"
tree="$1" pkgdir="${2%/}" outbin="$3" overlay="${4:-}"
GO="${GO:-go}"

[ -d "$tree/$pkgdir" ] || die "no package directory '$tree/$pkgdir'"
[ -z "$overlay" ] || [ -d "$overlay/$pkgdir" ] || die "no package directory '$overlay/$pkgdir' to take the benchmarks from"
case "$outbin" in /*) ;; *) outbin="$PWD/$outbin" ;; esac

# The module: the nearest go.mod at or above the package, within the tree.
mod="$pkgdir"
while [ ! -f "$tree/$mod/go.mod" ]; do
  [ "$mod" != . ] || die "no go.mod above '$pkgdir' in '$tree'"
  mod="$(dirname "$mod")"
done
if [ "$mod" = . ]; then rel="./$pkgdir"
elif [ "$mod" = "$pkgdir" ]; then rel=.
else rel="./${pkgdir#"$mod"/}"
fi

compile() {
  (cd "$tree/$mod" && GOWORK=off "$GO" test -c -o "$outbin" "$rel")
}

if [ -z "$overlay" ]; then
  compile
  exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/saved"

# copy <file>... — the head's versions over the base's, the base's saved.
copied=()
copy() {
  local f
  for f in "$@"; do
    if [ -e "$tree/$pkgdir/$f" ]; then cp -p "$tree/$pkgdir/$f" "$work/saved/$f"; fi
    cp -p "$overlay/$pkgdir/$f" "$tree/$pkgdir/$f"
    copied+=("$f")
  done
}

# restore — the base's package as it was.
restore() {
  local f
  for f in "${copied[@]+"${copied[@]}"}"; do
    if [ -e "$work/saved/$f" ]; then cp -p "$work/saved/$f" "$tree/$pkgdir/$f"; else rm -f "$tree/$pkgdir/$f"; fi
  done
  copied=()
  rm -f "$work"/saved/*
}

head_files=()
for path in "$overlay/$pkgdir"/*_bench_test.go; do
  [ -e "$path" ] || continue
  head_files+=("$(basename "$path")")
done

# The fast path: every head file at once.
if [ "${#head_files[@]}" -gt 0 ]; then
  copy "${head_files[@]}"
  if compile 2>"$work/err"; then
    echo "overlay=all"
    printf 'overlay-file=%s\n' "${head_files[@]}"
    exit 0
  fi
  printf 'bench-ab-build: the head'"'"'s bench files do not compile together on this base:\n' >&2
  sed 's/^/  /' "$work/err" >&2
  restore
fi

# One file at a time: the shared ones, then the new ones.
ordered=()
for f in "${head_files[@]+"${head_files[@]}"}"; do [ ! -e "$tree/$pkgdir/$f" ] || ordered+=("$f"); done
for f in "${head_files[@]+"${head_files[@]}"}"; do [ -e "$tree/$pkgdir/$f" ] || ordered+=("$f"); done
kept=()
for f in "${ordered[@]+"${ordered[@]}"}"; do
  copy "$f"
  if compile 2>"$work/err"; then
    kept+=("$f")
    copied=()
    rm -f "$work"/saved/*
  else
    printf 'bench-ab-build: %s left out, it does not compile on this base\n' "$f" >&2
    restore
  fi
done

if [ "${#kept[@]}" -gt 0 ]; then
  # The binary of the files kept, whatever the last attempt left behind.
  compile
  echo "overlay=partial"
  printf 'overlay-file=%s\n' "${kept[@]}"
  exit 0
fi
if compile; then
  echo "overlay=none"
  exit 0
fi
printf 'bench-ab-build: %s does not compile at all in %s\n' "$pkgdir" "$tree" >&2
exit 1
