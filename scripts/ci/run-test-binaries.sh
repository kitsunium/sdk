#!/bin/sh
# scripts/ci/run-test-binaries.sh BINDIR [TEST FLAG...] — run every test
# binary scripts/ci/test-binaries.sh wrote into BINDIR, each from its own
# package directory, as `go test` does (a suite reads its testdata/ and the
# sources around it by relative path). Run from the repository root.
#
# POSIX sh, nothing else: it runs inside FreeBSD, OpenBSD, NetBSD, OmniOS and
# Solaris guests and under busybox, none of which has bash or a Go toolchain.
#
# Each binary gets -test.timeout (TEST_TIMEOUT, default 5m): a binary run
# directly has no timeout of its own, and a hang must panic with every stack
# and let the next suite run. A binary is tried up to TEST_ATTEMPTS times
# (default 3) — the guests are virtual machines, and a timing-sensitive suite
# can lose one round to the hypervisor; a real defect fails every attempt. A
# suite that timed out is NOT tried again: a hang spends one timeout, never
# three, so it cannot eat the step's cap and leave the suites after it unrun
# and this summary unprinted. A suite that passed only on a retry is NAMED at
# the end, as a flake to answer, never silently absorbed. Stdin is /dev/null,
# so no suite can read the manifest this loop is reading.
#
# Exit 1 when any suite failed every attempt, naming each.

set -u

if [ "$#" -lt 1 ]; then
  echo "usage: run-test-binaries.sh BINDIR [TEST FLAG...]" >&2
  exit 2
fi

bindir=$(cd "$1" && pwd) || exit 2
shift
root=$(pwd)
attempts=${TEST_ATTEMPTS:-3}
timeout=${TEST_TIMEOUT:-5m}
kernel=$(uname -sr)

if [ ! -s "$bindir/manifest" ]; then
  echo "run-test-binaries.sh: $bindir/manifest is missing or empty — refusing to pass having run nothing" >&2
  exit 1
fi

now() { date +%s 2>/dev/null || echo 0; }

log="${TMPDIR:-/tmp}/run-test-binaries.$$.log"
trap 'rm -f "$log"' EXIT

failed=""
flaky=""
ran=0
start=$(now)
while read -r bin dir; do
  [ -n "$bin" ] || continue
  ran=$((ran + 1))
  t0=$(now)
  i=1
  ok=0
  hung=0
  while [ "$i" -le "$attempts" ]; do
    if (cd "$root/$dir" && "$bindir/$bin" -test.timeout="$timeout" "$@") </dev/null >"$log" 2>&1; then
      cat "$log"
      ok=1
      break
    fi
    cat "$log"
    if grep -q '^panic: test timed out after' "$log"; then
      echo "  ($dir timed out on $kernel: not tried again)"
      hung=1
      break
    fi
    echo "  (attempt $i of $dir failed on $kernel)"
    i=$((i + 1))
  done
  t1=$(now)
  if [ "$ok" -eq 1 ]; then
    echo "ok   $dir ($((t1 - t0))s)"
    [ "$i" -gt 1 ] && flaky="$flaky $dir"
  else
    if [ "$hung" -eq 1 ]; then
      echo "FAIL $dir ($((t1 - t0))s, timed out after $timeout)"
    else
      echo "FAIL $dir ($((t1 - t0))s, $attempts attempts)"
    fi
    failed="$failed $dir"
  fi
done <"$bindir/manifest"

echo "== $ran suites on $kernel in $(($(now) - start))s =="
for dir in $flaky; do
  echo "::warning::$dir passed only on a retry on $kernel"
done
if [ -n "$failed" ]; then
  for dir in $failed; do
    echo "::error::$dir fails on $kernel"
  done
  exit 1
fi
exit 0
