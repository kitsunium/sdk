#!/usr/bin/env bash
# scripts/release/check-published.sh — after the push, every module version a
# release names is one the module proxy serves and the go command can download
# (ADR 0171 §7, ADR 0009).
#
# A tag on origin is not yet a release a consumer resolves: the proxy fetches
# it on the first request, and that request can fail — a go.mod the proxy
# refuses, a tag it cannot read, a zip over its limits — while every step
# before this one is green. This is the step that asks, for each tag of
# release-manifest.json (the modules and the tombstones):
#
#   1. `<proxy>/<module>/@v/<version>.info` answers 200, retried with a
#      bounded backoff: the proxy keeps a negative answer for a few minutes,
#      so a request made just after the push can be told 404 for a version
#      that exists (CHECK_PUBLISHED_ATTEMPTS, default 10, every
#      CHECK_PUBLISHED_WAIT seconds, default 30 — five minutes at most);
#   2. `go mod download <module>@<version>` succeeds in a temporary module
#      with GOPROXY=<proxy>, GOFLAGS=-mod=mod and the checksum database on —
#      the go command fetches the .mod and the zip and checks them.
#
# Exit 0: every version served and downloaded. 1: one is not (each named on
# stderr; with $GITHUB_STEP_SUMMARY set, a table is appended to it). 64: bad
# usage. `--proxy=` (default https://proxy.golang.org) also takes a file://
# directory, which is how the suite runs it; `--no-sumdb` turns the checksum
# database off for such a proxy.

set -euo pipefail

PROXY="https://proxy.golang.org"
MANIFEST=""
SUMDB=1
for arg in "$@"; do
  case "$arg" in
    --proxy=*) PROXY="${arg#--proxy=}" ;;
    --manifest=*) MANIFEST="${arg#--manifest=}" ;;
    --no-sumdb) SUMDB=0 ;;
    -h | --help)
      sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "check-published: unknown argument $arg" >&2
      exit 64
      ;;
  esac
done
if [ -z "$MANIFEST" ] || [ ! -r "$MANIFEST" ]; then
  echo "check-published: --manifest=<release-manifest.json> is required and must be readable" >&2
  exit 64
fi
for tool in go jq curl; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "check-published: $tool is not on PATH" >&2
    exit 64
  }
done

ATTEMPTS="${CHECK_PUBLISHED_ATTEMPTS:-10}"
WAIT="${CHECK_PUBLISHED_WAIT:-30}"
SDK="github.com/kitsunium/sdk"

# The module path and version of a tag: the root's `vX.Y.Z`, any other
# `<dir>/vX.Y.Z`, a tombstone's included.
pairs="$(jq -r '(.modules[]? | "\(.module) \(.version)"), (.tombstones[]? | . as $t | ($t | sub("/v[0-9][^/]*$"; "")) as $d | "\("github.com/kitsunium/sdk/" + $d) \($t | sub("^.*/"; ""))")' "$MANIFEST")"
if [ -z "$pairs" ]; then
  echo "check-published: $MANIFEST names no module and no tombstone — nothing was checked, which is no pass" >&2
  exit 1
fi

escape() { sed -E 's/([A-Z])/!\L\1/g' <<<"$1"; }

work="$(mktemp -d)"
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
printf 'module example.com/check-published\n\ngo 1.21\n' >"$work/go.mod"

sumenv=(GOFLAGS=-mod=mod)
if [ "$SUMDB" -eq 0 ]; then sumenv=(GONOSUMDB="$SDK/*" GOSUMDB=off); fi

fail=0
rows=""
while read -r module version; do
  [ -z "$module" ] && continue
  info="$PROXY/$(escape "$module")/@v/$version.info"
  served=0
  for ((i = 1; i <= ATTEMPTS; i++)); do
    if curl -fsSL --max-time 30 -o /dev/null "$info" 2>/dev/null; then
      served=1
      break
    fi
    [ "$i" -lt "$ATTEMPTS" ] && sleep "$WAIT"
  done
  if [ "$served" -eq 0 ]; then
    echo "check-published: $module@$version — the proxy does not serve $info after $ATTEMPTS attempts" >&2
    rows="$rows| \`$module\` | \`$version\` | not served |"$'\n'
    fail=1
    continue
  fi
  out=""
  rc=0
  out="$(cd "$work" && env GOWORK=off GOFLAGS=-mod=mod GOPROXY="$PROXY" GOTOOLCHAIN=local \
    GOPATH="$work/gopath" GOMODCACHE="$work/gopath/pkg/mod" "${sumenv[@]}" \
    go mod download -json "$module@$version" 2>&1)" || rc=$?
  if [ "$rc" -ne 0 ]; then
    echo "check-published: $module@$version — go mod download failed:" >&2
    sed 's/^/  /' <<<"$out" >&2
    rows="$rows| \`$module\` | \`$version\` | served, not downloadable |"$'\n'
    fail=1
    continue
  fi
  echo "ok $module@$version — served and downloaded"
  rows="$rows| \`$module\` | \`$version\` | ok |"$'\n'
done <<<"$pairs"

if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Published on the module proxy"
    echo
    echo "| module | version | |"
    echo "|---|---|---|"
    printf '%s' "$rows"
  } >>"$GITHUB_STEP_SUMMARY"
fi
exit "$fail"
