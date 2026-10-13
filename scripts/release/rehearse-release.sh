#!/usr/bin/env bash
# scripts/release/rehearse-release.sh — the release guard (ADR 0171 §7): the
# release the workflow is about to cut, cut first in a throwaway clone with
# `cut-tags.sh --no-push` and rehearsed on a file proxy by
# `proxy-rehearsal.sh --manifest`, before anything reaches origin.
#
# It reads on stdin what cut-tags.sh reads (compute-bumps.sh's module
# directories, or `sdk`) and passes every argument through to cut-tags.sh
# (`--bump=`, `--range=`, `--allow-bootstrap`), adding `--no-push` and its own
# `--manifest=`. The clone's origin is this repository, so the prune-fetch reads
# the tags this checkout holds — origin's, in the workflow — and nothing the
# rehearsal tags leaves the clone.
#
# What the rehearsal holds the release to is proxy-rehearsal.sh's: every family
# of the release added to a consumer pinned to the monolithic root, the root
# lifted to its floor; `go get -u ./...` from that root ending on the internal
# module; a consumer on …/pkg v0.17.0 adding a family with one `go get`; every
# family the release does NOT cut, at its last tag, building against this
# release's internal and families; and the control's ambiguous import.
#
# Exit 0: rehearsed green, or a release with no pkg family (proxy-rehearsal's
# exit 3, said on stderr). Otherwise cut-tags.sh's or proxy-rehearsal.sh's own
# status — 3 a held bootstrap, 65 a refused size, 1 a consumer that did not
# build. `KEEP=1` keeps the clone and the proxy.

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
work="$(mktemp -d)"
if [ "${KEEP:-0}" != 1 ]; then
  trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
else
  echo "rehearse-release: work directory kept at $work" >&2
fi

if ! git clone -q --no-local "$root" "$work/repo"; then
  echo "rehearse-release: could not clone $root" >&2
  exit 2
fi
head="$(git -C "$root" rev-parse HEAD)"
cd "$work/repo" || exit 2
git checkout -q --detach "$head" || exit 2
git config user.name "${GIT_AUTHOR_NAME:-$(git -C "$root" config user.name || echo release)}"
git config user.email "${GIT_AUTHOR_EMAIL:-$(git -C "$root" config user.email || echo release@users.noreply.github.com)}"

rc=0
bash scripts/release/cut-tags.sh --no-push --manifest="$work/release-manifest.json" "$@" >"$work/tags.txt" || rc=$?
if [ "$rc" -ne 0 ]; then
  echo "rehearse-release: cut-tags.sh --no-push stopped (exit $rc) — nothing to rehearse" >&2
  exit "$rc"
fi
if [ ! -s "$work/tags.txt" ]; then
  echo "rehearse-release: the cut tagged nothing — nothing to rehearse"
  exit 0
fi
echo "rehearse-release: the cut would tag $(paste -sd' ' "$work/tags.txt")"
rc=0
bash scripts/release/proxy-rehearsal.sh --manifest="$work/release-manifest.json" \
  --ref="$(jq -r .release_commit "$work/release-manifest.json")" || rc=$?
case "$rc" in
  0) echo "rehearse-release: green" ;;
  3)
    echo "rehearse-release: the release cuts no pkg family — the file-proxy consumers do not apply" >&2
    rc=0
    ;;
esac
exit "$rc"
