#!/usr/bin/env bash
#
# hooks-test.sh — run the BATS suite that guards .githooks/.
#
# .githooks/commit-msg is the repository's enforcement of the no-AI-attribution
# policy, and it shipped with a defect whose failure mode is inverted: it did
# not merely stop blocking, it ALLOWED exactly what it exists to refuse.
#
#   if printf '%s' "$MSG" | grep -qiE "$pattern"; then … exit 1; fi
#
# `grep -q` exits on its first match, the writer takes SIGPIPE, `set -o
# pipefail` turns the pipeline into 141, and 141 is false — so the `if` does not
# fire. `set -e` does not rescue it: a failing command in an `if` condition is
# exempt by design. Measured before the fix, `Co-Authored-By: Claude` near the
# top of the message with filler after it, 40 runs per cell, "pass" meaning the
# commit went through:
#
#   body      pattern early   pattern late
#   1 000     0/40            0/40
#   64 000    0/40            0/40
#   70 000    1/40            0/40
#   96 000    27/40           0/40
#   400 000   40/40           0/40
#
# The hook worked below the pipe buffer, which is why nobody noticed, and past a
# few hundred kilobytes it failed every time. After the fix: 0/40 at 400 KB.
#
# The right-hand column is the control. With the pattern at the END, grep must
# read the whole input, so there is no early exit and no signal — the gate held
# at every size. That asymmetry IS the bug, and the suite pins both halves.
#
# Run: scripts/hooks-test.sh
#      make hooks-check
#
# Needs only bats and coreutils — no Go, no jq, no Bazel, no network.

set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"

SUITES=(
  "$HERE/test-commit-msg-hook.bats"
)

if ! command -v bats >/dev/null 2>&1; then
  cat >&2 <<'EOF'
hooks-test: bats-core is not on PATH.

Install it with one of:

    sudo apt-get install -y bats     # Debian / Ubuntu — what CI uses
    brew install bats-core           # macOS
    npm install -g bats              # anywhere node is available

Then re-run: make hooks-check
EOF
  exit 127
fi

printf 'hooks-test: %s (%s)\n' "$(bats --version)" "$(command -v bats)"

for suite in "${SUITES[@]}"; do
  if [ ! -f "$suite" ]; then
    printf 'hooks-test: missing suite %s\n' "$suite" >&2
    printf '  either restore it or drop it from SUITES in %s\n' "$0" >&2
    exit 1
  fi
done

bats "${SUITES[@]}"
