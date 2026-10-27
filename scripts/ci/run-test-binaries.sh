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
# and let the next suite run.
#
# A suite test-binaries.sh cut into parts is one manifest line per part: the
# binary and its directory, then the part as k/n, its -test.run and its
# -test.skip ("-" for none), passed before the flags given here. Each part is
# a run of its own, with its own timeout and attempts, named DIR[k/n].
#
# A suite that fails is tried again, up to TEST_ATTEMPTS times in all (default
# 3), and ONLY to tell a defect from a flake: a suite failing every attempt
# fails, and a suite that failed and then passed FAILS TOO, named as flaky —
# a flake is a defect to fix, and a lane that let it pass would hide it: CI
# detects a flaky test, it never masks one. The retry never turns a red suite
# green. A suite that timed out is NOT tried again: a hang
# spends one timeout, never three, so it cannot eat the step's cap and leave
# the suites after it unrun and this summary unprinted. Stdin is /dev/null,
# so no suite can read the manifest this loop is reading.
#
# Exit 1 when any suite failed on any attempt, naming each: "fails" for one
# that never passed, "is flaky" for one that passed only on a retry.

set -u
# A part's regular expressions reach its binary as written: never as globs.
set -f

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
while read -r bin dir part run skip; do
  [ -n "$bin" ] || continue
  ran=$((ran + 1))
  name=$dir
  sel=""
  if [ -n "$part" ]; then
    name="${dir}[$part]"
    [ "$run" = - ] || sel="-test.run=$run"
    [ "$skip" = - ] || sel="$sel -test.skip=$skip"
  fi
  t0=$(now)
  i=1
  ok=0
  hung=0
  while [ "$i" -le "$attempts" ]; do
    # shellcheck disable=SC2086 # $sel is the part's flags, split on purpose
    if (cd "$root/$dir" && "$bindir/$bin" -test.timeout="$timeout" $sel "$@") </dev/null >"$log" 2>&1; then
      cat "$log"
      ok=1
      break
    fi
    cat "$log"
    if grep -q '^panic: test timed out after' "$log"; then
      echo "  ($name timed out on $kernel: not tried again)"
      hung=1
      break
    fi
    echo "  (attempt $i of $name failed on $kernel)"
    i=$((i + 1))
  done
  t1=$(now)
  if [ "$ok" -eq 1 ] && [ "$i" -eq 1 ]; then
    echo "ok   $name ($((t1 - t0))s)"
  elif [ "$ok" -eq 1 ]; then
    echo "FLAKY $name ($((t1 - t0))s, failed $((i - 1)) of $i attempts)"
    flaky="$flaky $name:$i"
  else
    if [ "$hung" -eq 1 ]; then
      echo "FAIL $name ($((t1 - t0))s, timed out after $timeout)"
    else
      echo "FAIL $name ($((t1 - t0))s, $attempts attempts)"
    fi
    failed="$failed $name"
  fi
done <"$bindir/manifest"

echo "== $ran suites on $kernel in $(($(now) - start))s =="
for entry in $flaky; do
  echo "::error::${entry%:*} is flaky on $kernel: it failed, then passed on attempt ${entry##*:}; a flake is a defect to fix, never a pass"
done
for dir in $failed; do
  echo "::error::$dir fails on $kernel"
done
if [ -n "$failed$flaky" ]; then
  exit 1
fi
exit 0
