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
# Usage: run.sh [crash iterations] [negative] [fs] [preallocate]
# (default 20, 0, ext4, zerofill). With negative=1 the writer acknowledges
# BEFORE its sync, and the run must FAIL: the lane's negative control. fs is
# ext4 or xfs; preallocate is zerofill or fallocate, the segments'
# DataDirOptions.Preallocate (#922), and with fallocate the file log's suite
# and its fuzz seeds run on that filesystem first. Output: RESULT lines on
# stdout, recorded in RESULTS.md. DMCRYPT_RAM=1 backs the loop device with
# a 3 GiB tmpfs instead of an 8 GiB file of the container: the run then
# writes nothing to the host's disk. DMCRYPT_HOSTDISK=1 puts a
# DMCRYPT_IMG_SIZE (4G) image in the work directory, under TMPDIR, instead.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
pkg="$(cd "$here/../.." && pwd)"
iters="${1:-20}"
negative="${2:-0}"
fs="${3:-ext4}"
prealloc="${4:-zerofill}"
suite=""
if [ "$prealloc" = fallocate ]; then
	# Every test and fuzz target of the file log's suite files, by name.
	suite="^($(cd "$pkg" && grep -ho '^func \(Test\|Fuzz\)[A-Za-z0-9_]*' \
		filelog_internal_test.go filelog_range_internal_test.go filelog_fuzz_internal_test.go prealloc_internal_test.go |
		cut -d' ' -f2 | paste -sd'|' -))\$"
fi
arch="$(docker info --format '{{.Architecture}}')"
case "$arch" in
aarch64 | arm64) goarch=arm64 ;;
*) goarch=amd64 ;;
esac
out="$(mktemp -d)"
trap 'rm -rf "$out"' EXIT
(cd "$pkg" && GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go test -c -tags dmcrypt -o "$out/consensus.test" .)
cp "$here/inner.sh" "$out/inner.sh"
ram=()
if [ "${DMCRYPT_HOSTDISK:-0}" = 1 ]; then
	# The image on the work directory (under TMPDIR) instead of the
	# container's own layer: a Linux host whose root disk is small.
	ram+=(-e DMCRYPT_IMG=/work/disk.img -e DMCRYPT_IMG_SIZE="${DMCRYPT_IMG_SIZE:-4G}")
fi
if [ "${DMCRYPT_RAM:-0}" = 1 ]; then
	ram=(--tmpfs /ram:rw,size=3g -e DMCRYPT_IMG=/ram/disk.img -e DMCRYPT_IMG_SIZE=2900M)
fi
docker run --rm --privileged ${ram[@]+"${ram[@]}"} -v "$out:/work" debian:stable-slim bash /work/inner.sh "$iters" "$negative" "$fs" "$prealloc" "$suite"
