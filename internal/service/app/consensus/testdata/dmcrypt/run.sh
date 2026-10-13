#!/usr/bin/env bash
# testdata/dmcrypt/run.sh — the dm-crypt lane of ADR 0152 (D15, R9-03).
#
# Builds the consensus package's dmcrypt-tagged test binary for Linux, then
# runs inner.sh in a privileged Linux container (Docker Desktop's VM is
# enough): a loop device, LUKS2 dm-crypt, a dm-linear "cut" device on top,
# ext4 on that. It measures fsync latency per batch, then cuts power through
# dmsetup — the cut device is swapped for an error target while a writer
# runs, so every write not yet on the crypt device is lost — and checks that
# the log reopens without quarantine with every acknowledged entry intact.
#
# Usage: run.sh [crash iterations] [negative]   (default 20, 0). With
# negative=1 the writer acknowledges BEFORE its sync, and the run must FAIL:
# the lane's negative control. Output: RESULT lines on stdout, recorded in
# RESULTS.md.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
pkg="$(cd "$here/../.." && pwd)"
iters="${1:-20}"
negative="${2:-0}"
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in
aarch64 | arm64) goarch=arm64 ;;
*) goarch=amd64 ;;
esac
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
(cd "$pkg" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -tags dmcrypt -o "$out/consensus.test" .)
cp "$here/inner.sh" "$out/inner.sh"
docker run --rm --privileged -v "$out:/work" debian:stable-slim bash /work/inner.sh "$iters" "$negative"
