#!/usr/bin/env bash
# from-zero.sh — rebuild the SDK from design/ alone, in a temporary copy of the
# tree, and prove it is the tree (`make from-zero`; ADR 0169). It is stage 7 of
# "kit regenerates the SDK from zero", one step past `make regen`:
#
#   1. copy every file git tracks or would track (untracked, not ignored), as
#      the working tree holds it — never the working tree itself, which this
#      script does not write a byte of;
#   2. in the copy, delete every file whose FIRST line is kit's generated
#      header and clear kit's section of tools/alloc-lane-targets.txt, exactly
#      as scripts/regen.sh does;
#   3. `kit gen -stubs`: the design writes every generated file back, and a
#      body that panics (stubs_gen.go) for each impl the design names that no
#      hand-written file declares — then every module of the census must
#      COMPILE (go build and go vet, tests type-checked too). The stubs are
#      counted per package: where every body exists, there are none;
#   4. plain `kit gen` (which removes the stubs) then `make api`, and the copy
#      must equal the working tree: no file added, none missing, none changed.
#
# What it proves that `make regen` does not: regen deletes the generated files
# in the working tree and so requires a clean one; this one never touches it,
# runs over uncommitted work, and shows the intermediate state — the design and
# the hand-written bodies, nothing else — compiles.
#
# Local only: it needs kit at the version design/sdk.yaml pins, as `make regen`
# does, and `make api` runs bazel (gazelle) in the copy, a workspace of its
# own. FROM_ZERO_KEEP=1 keeps the copy and prints where it is.
#
# Runs under macOS's /bin/bash 3.2 and the BSD tools (scripts/CLAUDE.md).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"

die() {
  printf 'from-zero: %s\n' "$1" >&2
  exit 1
}

# shellcheck source=scripts/kit-pinned.sh
. scripts/kit-pinned.sh
kit_pinned from-zero || exit 1
printf 'from-zero: kit %s\n' "$KIT_PINNED"

header=$KIT_HEADER

work="$(mktemp -d "${TMPDIR:-/tmp}/from-zero.XXXXXX")"
work="$(cd "$work" && pwd -P)"
lists="$(mktemp -d "${TMPDIR:-/tmp}/from-zero-lists.XXXXXX")"
cleanup() {
  rm -rf "$lists"
  if [ "${FROM_ZERO_KEEP:-}" = 1 ]; then
    printf 'from-zero: the copy is kept at %s\n' "$work" >&2
    return
  fi
  # bazel keys its output base by the workspace's path, and every run's copy
  # is a new path: expunge it, or each run leaves one behind (its server, its
  # external repositories, its outputs, read-only, are the copy's own).
  if [ -f "$work/MODULE.bazel" ] && command -v bazel >/dev/null 2>&1; then
    (cd "$work" && bazel clean --expunge >/dev/null 2>&1) || true
  fi
  chmod -R u+w "$work" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT


# 1. The copy: what git tracks or would track, as it is on disk. A tracked file
#    deleted in the working tree is not there to copy, and is not expected back.
git ls-files -z -c -o --exclude-standard | while IFS= read -r -d '' f; do
  if [ -e "$f" ] || [ -L "$f" ]; then
    printf '%s\0' "$f"
  fi
done >"$lists/files0"
# The lists below are a path per line: a path holding a newline cannot be
# one, and is refused rather than split into two paths that do not exist.
if [ "$(tr -cd '\n' <"$lists/files0" | wc -c | tr -d ' ')" -ne 0 ]; then
  die "a path git lists holds a newline; rename it, or run make regen"
fi
tr '\0' '\n' <"$lists/files0" | LC_ALL=C sort -u >"$lists/before"
# The modules: every go.mod of the copy — untracked ones too, which the copy
# holds and scripts/ci/go-modules.sh (git's tracked census) does not —,
# testdata left out as the census leaves it out.
modules="$(grep -E '(^|/)go\.mod$' "$lists/before" | grep -v '/testdata/' | grep -v '^testdata/' | sed -e 's#/\{0,1\}go\.mod$##' -e 's#^$#.#' | LC_ALL=C sort)"
tar --null -T "$lists/files0" -cf - | (cd "$work" && tar -xpf -)
printf 'from-zero: copied %s files to %s\n' "$(wc -l <"$lists/before" | tr -d ' ')" "$work"

cd "$work"

# 2. Every file kit generated, by its header, never its name.
: >"$lists/generated"
while IFS= read -r f; do
  case "$f" in
  # Only a Go file can open with kit's header: every other file is skipped
  # unread.
  *.go) ;;
  *) continue ;;
  esac
  first="$(head -n 1 "$f")"
  if [[ $first =~ $header ]]; then
    printf '%s\n' "$f" >>"$lists/generated"
  fi
done <"$lists/before"
n="$(wc -l <"$lists/generated" | tr -d ' ')"
[ "$n" -gt 0 ] || die "no file carries the kit header; nothing to rebuild"
while IFS= read -r f; do
  rm -f -- "$f"
done <"$lists/generated"
section=tools/alloc-lane-targets.txt
kit_clear_section "$section"
printf 'from-zero: deleted %s generated files and kit'"'"'s section of %s\n' "$n" "$section"

# 3. The design and the hand-written bodies, stubs where a body is missing.
printf 'from-zero: kit gen -stubs\n'
kit gen -stubs >"$lists/gen-stubs.log" || {
  cat "$lists/gen-stubs.log" >&2
  die "kit gen -stubs failed"
}
find . -name stubs_gen.go -type f | LC_ALL=C sort >"$lists/stubs"
total=0
if [ -s "$lists/stubs" ]; then
  printf 'from-zero: stubs, per package:\n'
  while IFS= read -r f; do
    c="$(grep -c 'panic("unimplemented: ' "$f" || true)"
    total=$((total + c))
    printf '  %4d  %s\n' "$c" "$(dirname "${f#./}")"
  done <"$lists/stubs"
fi
printf 'from-zero: %s stub(s) in %s package(s)\n' "$total" "$(wc -l <"$lists/stubs" | tr -d ' ')"

printf 'from-zero: compiling every module from the design and the bodies\n'
failed=""
for m in $modules; do
  if ! (cd "$m" && GOWORK=off go build ./... && GOWORK=off go vet ./...) >"$lists/build.log" 2>&1; then
    printf 'from-zero: module %s does not compile:\n' "$m" >&2
    head -n 40 "$lists/build.log" >&2
    failed="$failed $m"
  fi
done
[ -z "$failed" ] || die "from the design alone, these modules do not compile:$failed"
printf 'from-zero: every module compiles\n'

# 4. The generation proper, then make api.
printf 'from-zero: kit gen\n'
kit gen >"$lists/gen.log" || {
  cat "$lists/gen.log" >&2
  die "kit gen failed"
}
printf 'from-zero: make api\n'
make --no-print-directory api >"$lists/api.log" 2>&1 || {
  tail -n 40 "$lists/api.log" >&2
  die "make api failed"
}

# The copy's files, ignored ones left out as git would leave them out: bazel's
# convenience links, its outputs.
find . \( -type f -o -type l \) ! -path './.git/*' | sed 's#^\./##' | LC_ALL=C sort >"$lists/found"
(cd "$root" && git check-ignore --stdin <"$lists/found" || true) | LC_ALL=C sort >"$lists/ignored"
LC_ALL=C comm -23 "$lists/found" "$lists/ignored" >"$lists/after"

added="$(LC_ALL=C comm -13 "$lists/before" "$lists/after")"
missing="$(LC_ALL=C comm -23 "$lists/before" "$lists/after")"
changed=""
while IFS= read -r f; do
  if [ -e "$f" ] && ! cmp -s "$root/$f" "$f"; then
    changed="$changed$f
"
  fi
done <"$lists/before"
if [ -n "$added$missing$changed" ]; then
  [ -z "$added" ] || printf 'from-zero: added:\n%s\n' "$added" >&2
  [ -z "$missing" ] || printf 'from-zero: missing:\n%s\n' "$missing" >&2
  if [ -n "$changed" ]; then
    printf 'from-zero: changed:\n%s' "$changed" >&2
    printf '%s' "$changed" | head -n 5 | while IFS= read -r f; do
      diff -u "$root/$f" "$f" | head -n 40 >&2 || true
    done
  fi
  die "the tree rebuilt from design/ is not the working tree"
fi
printf 'from-zero: %s generated files rebuilt from design/, %s stub(s) on the way, zero diff\n' "$n" "$total"
