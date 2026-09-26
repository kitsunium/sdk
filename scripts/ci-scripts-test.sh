#!/usr/bin/env bash
#
# ci-scripts-test.sh — run the BATS suites that guard scripts/ci/: the module
# census every module-looping lane reads (go-modules.sh, ADR 0137) and the
# govulncheck gate built on it (vuln-check.sh, ADR 0136).
#
# A suite nothing runs is not a test suite (ADR 0088), so this has a make
# target (`ci-scripts-check`), that target is in scripts/ci-gates-check.sh's
# GATES, and the shell-gates job of bazel-ci.yml runs it.
#
# Globbed, never enumerated, for the reason release-scripts-test.sh gives: a
# third suite added beside these must not be committed, reviewed and executed
# by nothing. bats is not fetched here; see release-scripts-test.sh for why.
#
# Run: scripts/ci-scripts-test.sh
#      make ci-scripts-check

set -euo pipefail

SUITE_DIR="$(cd "$(dirname "$0")/ci" && pwd)"

SUITES=()
for suite in "$SUITE_DIR"/*.bats; do
  SUITES+=("$suite")
done

if [ "${#SUITES[@]}" -eq 0 ] || [ ! -e "${SUITES[0]}" ]; then
  printf 'ci-scripts-test: no *.bats suite found in %s\n' "$SUITE_DIR" >&2
  printf '  the runner is wired but there is nothing for it to run\n' >&2
  exit 1
fi

if ! command -v bats >/dev/null 2>&1; then
  cat >&2 <<'EOF'
ci-scripts-test: bats-core is not on PATH.

Install it with one of:

    sudo apt-get install -y bats     # Debian / Ubuntu — what CI uses
    brew install bats-core           # macOS
    npm install -g bats              # anywhere node is available

Then re-run: make ci-scripts-check
EOF
  exit 127
fi

# The census tests build throwaway repositories, so git is a prerequisite, and
# under CI a missing one must not turn into skipped rows and a green gate.
if ! command -v git >/dev/null 2>&1; then
  printf 'ci-scripts-test: git is not on PATH — the census suite cannot run\n' >&2
  exit 1
fi

printf 'ci-scripts-test: %s (%s)\n' "$(bats --version)" "$(command -v bats)"

bats "${SUITES[@]}"
