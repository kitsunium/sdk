#!/usr/bin/env bash
#
# bench-ab-tree.sh REF DEST — checks REF out, detached, as a worktree of the
# current repository at DEST, and prints the commit it resolved to. A ref the
# clone does not have — the base of a shallow checkout — is fetched first from
# BENCH_AB_REMOTE (default origin), one commit deep: a full SHA or a ref name.
#
# Exit 0; 1 when REF cannot be resolved or fetched; 2 on bad usage.

set -euo pipefail

die() {
  printf 'bench-ab-tree: %s\n' "$*" >&2
  exit 2
}

[ $# -eq 2 ] || die "usage: bench-ab-tree.sh REF DEST"
ref="$1" dest="$2"
remote="${BENCH_AB_REMOTE:-origin}"
[[ "$ref" =~ ^[A-Za-z0-9._/-]+$ ]] && [[ "$ref" != -* ]] || die "ref '$ref': a commit or a ref name"
[ ! -e "$dest" ] || die "'$dest' exists"

if ! sha="$(git rev-parse --verify -q "$ref^{commit}")"; then
  if ! git fetch -q --no-tags --depth=1 "$remote" "$ref"; then
    printf 'bench-ab-tree: %s cannot be fetched from %s\n' "$ref" "$remote" >&2
    exit 1
  fi
  if ! sha="$(git rev-parse --verify -q "$ref^{commit}")"; then
    sha="$(git rev-parse --verify -q 'FETCH_HEAD^{commit}')" || {
      printf 'bench-ab-tree: %s fetched, yet names no commit\n' "$ref" >&2
      exit 1
    }
  fi
fi

git worktree add -q --detach "$dest" "$sha" >&2
echo "$sha"
