#!/usr/bin/env bash
# scripts/ci/bazel-remote-cache.sh EVENT REF RCFILE — the optional Bazel
# remote cache of the `bazel` job (ADR 0250), appended to RCFILE (the
# runner's ~/.bazelrc) as `build` lines, so every Bazel command of the job —
# build, test, coverage, the queries, target-determinator's — reads it.
#
# Off unless BAZEL_REMOTE_CACHE_URL names one (grpc://, grpcs://, http://,
# https://): with no URL nothing is written and the job runs exactly as it
# did. The repository is public, so a pull request — a fork's above all —
# never writes to it:
#
#   - it WRITES only on the events that test what lands: a push to the default
#     branch (SCOPE_DEFAULT_BRANCH, main), a merge queue entry and the nightly
#     run — and only with a write credential, BAZEL_REMOTE_CACHE_WRITE_HEADER
#     (`Name=Value`, e.g. `Authorization=Basic <base64>`) or the client
#     certificate BAZEL_REMOTE_CACHE_CLIENT_CERT and its key
#     BAZEL_REMOTE_CACHE_CLIENT_KEY (PEM), which the workflow hands to this
#     step on those events alone;
#   - every other run READS only (`--remote_upload_local_results=false`),
#     with BAZEL_REMOTE_CACHE_READ_HEADER when set, else anonymously when
#     BAZEL_REMOTE_CACHE_READ is `anonymous` — the server accepting reads
#     without a credential —, else not at all. A fork's run receives no
#     secret: the server, which refuses an unauthenticated write, is what
#     keeps it out; the flag only spares it the attempt.
#
# BAZEL_REMOTE_CACHE_CA (PEM) names a private certificate authority. Prints
# `remote-cache=off|read-only|read-write` for $GITHUB_OUTPUT and the reason
# on stderr. A malformed configuration turns the cache off with a warning:
# a cache is never a reason for a run to fail. Exit 64 on bad usage.

set -euo pipefail

if [ "$#" -ne 3 ] || [ -z "$3" ]; then
  echo "usage: $0 EVENT REF RCFILE" >&2
  exit 64
fi
event="$1" ref="$2" rc="$3"
default="${SCOPE_DEFAULT_BRANCH:-main}"
url="${BAZEL_REMOTE_CACHE_URL:-}"

off() {
  echo "remote-cache=off"
  echo "bazel-remote-cache: off — $1" >&2
}

if [ -z "$url" ]; then
  off "BAZEL_REMOTE_CACHE_URL is not set"
  exit 0
fi
case "$url" in
  grpc://* | grpcs://* | http://* | https://*) ;;
  *)
    echo "::warning title=Bazel remote cache off::BAZEL_REMOTE_CACHE_URL is not a grpc://, grpcs://, http:// or https:// URL" >&2
    off "the URL is malformed"
    exit 0
    ;;
esac
case "$url" in
  *[[:space:]]*)
    echo "::warning title=Bazel remote cache off::BAZEL_REMOTE_CACHE_URL holds white space" >&2
    off "the URL is malformed"
    exit 0
    ;;
esac

trusted=0
case "$event" in
  merge_group | schedule) trusted=1 ;;
  push) [ "$ref" = "refs/heads/$default" ] && trusted=1 ;;
esac

write_header="${BAZEL_REMOTE_CACHE_WRITE_HEADER:-}"
cert="${BAZEL_REMOTE_CACHE_CLIENT_CERT:-}"
key="${BAZEL_REMOTE_CACHE_CLIENT_KEY:-}"
read_header="${BAZEL_REMOTE_CACHE_READ_HEADER:-}"
anonymous=0
[ "${BAZEL_REMOTE_CACHE_READ:-}" = anonymous ] && anonymous=1

# A header is one `Name=Value` line: anything else would be a second flag.
for h in "$write_header" "$read_header"; do
  case "$h" in
    *$'\n'* | *$'\r'*)
      echo "::warning title=Bazel remote cache off::a remote cache header holds a line break" >&2
      off "a header is malformed"
      exit 0
      ;;
  esac
  if [ -n "$h" ] && [[ "$h" != ?*=?* ]]; then
    echo "::warning title=Bazel remote cache off::a remote cache header is not Name=Value" >&2
    off "a header is malformed"
    exit 0
  fi
done
if { [ -n "$cert" ] && [ -z "$key" ]; } || { [ -z "$cert" ] && [ -n "$key" ]; }; then
  echo "::warning title=Bazel remote cache off::BAZEL_REMOTE_CACHE_CLIENT_CERT and BAZEL_REMOTE_CACHE_CLIENT_KEY go together" >&2
  off "a client certificate without its key, or the reverse"
  exit 0
fi

mode="" header=""
use_cert=0
if [ "$trusted" -eq 1 ] && { [ -n "$write_header" ] || [ -n "$cert" ]; }; then
  mode=read-write
  header="$write_header"
  [ -n "$cert" ] && use_cert=1
elif [ -n "$read_header" ]; then
  mode=read-only
  header="$read_header"
elif [ "$anonymous" -eq 1 ]; then
  mode=read-only
else
  if [ "$trusted" -eq 1 ]; then
    off "no write credential, and anonymous reads are not declared (BAZEL_REMOTE_CACHE_READ=anonymous)"
  else
    off "no read credential, and anonymous reads are not declared (BAZEL_REMOTE_CACHE_READ=anonymous)"
  fi
  exit 0
fi

dir="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/bazel-remote-cache"
umask 077
mkdir -p "$dir"

# rc_word VALUE — VALUE as one word of an rc file. Bazel tokenizes a line of an
# rc file by the rules of the Bourne shell, so `Authorization=Basic <base64>`
# written bare is cut at its space and the credential becomes a flag of its
# own; single quotes keep every character, and a quote inside is closed,
# escaped and reopened.
rc_word() {
  local v="$1" q="'"
  printf "'%s'" "${v//$q/$q\\$q$q}"
}

# Written aside, appended at once: a run never reads half a configuration.
block="$dir/bazelrc"
{
  echo "# The optional remote cache (scripts/ci/bazel-remote-cache.sh): $mode."
  echo "build --remote_cache=$url"
  if [ "$mode" = read-write ]; then
    echo "build --remote_upload_local_results=true"
  else
    echo "build --remote_upload_local_results=false"
  fi
  # A slow or absent cache costs a bounded wait, never the run.
  echo "build --remote_timeout=60s"
  if [ -n "$header" ]; then
    echo "build $(rc_word "--remote_header=$header")"
  fi
  if [ "$use_cert" -eq 1 ]; then
    printf '%s\n' "$cert" >"$dir/client.crt"
    printf '%s\n' "$key" >"$dir/client.key"
    echo "build --tls_client_certificate=$dir/client.crt"
    echo "build --tls_client_key=$dir/client.key"
  fi
  if [ -n "${BAZEL_REMOTE_CACHE_CA:-}" ]; then
    printf '%s\n' "$BAZEL_REMOTE_CACHE_CA" >"$dir/ca.crt"
    echo "build --tls_certificate=$dir/ca.crt"
  fi
} >"$block"
cat "$block" >>"$rc"

echo "remote-cache=$mode"
echo "bazel-remote-cache: $mode" >&2
