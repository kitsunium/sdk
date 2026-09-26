#!/usr/bin/env bats
# BATS tests for scripts/ci/: the module census every module-looping lane reads
# (go-modules.sh, ADR 0137) and the govulncheck gate built on it (vuln-check.sh,
# ADR 0136). Run by `make ci-scripts-check` in the shell-gates job.
#
# The census is tested in throwaway repositories — each test copies the script
# into one, because the script answers for the repository it lives in. The
# scanner is a stub that answers per module from marker files, so every verdict
# govulncheck can give is reproduced without a network.

setup() {
  CI_DIR="$(cd "$BATS_TEST_DIRNAME" && pwd)"
  REPO_ROOT="$(cd "$CI_DIR/../.." && pwd)"
  WORK="$BATS_TEST_TMPDIR/work"
  mkdir -p "$WORK"
}

# fixture_repo — a repository whose scripts/ci/ is this one, with no module yet.
fixture_repo() {
  cd "$WORK"
  git init -q .
  mkdir -p scripts/ci
  cp "$CI_DIR/go-modules.sh" "$CI_DIR/vuln-check.sh" scripts/ci/
}

# module <dir> — a tracked go.mod in <dir>.
module() {
  mkdir -p "$1"
  printf 'module example.com/%s\n\ngo 1.27\n' "$(basename "$1")" >"$1/go.mod"
  git add "$1/go.mod"
}

# ── go-modules.sh ───────────────────────────────────────────────────────────

# #242: `./...` stops at the first nested go.mod, so a lane reaches only the
# modules somebody listed. The census finds them at any depth, root included.
@test "census: the root and every nested module, at any depth, sorted" {
  fixture_repo
  module .
  module tools/genindex
  module tools/sdkguard
  module internal/kernel
  run bash scripts/ci/go-modules.sh
  [ "$status" -eq 0 ]
  [ "$output" = $'.\ninternal/kernel\ntools/genindex\ntools/sdkguard' ]
}

# A go.mod under testdata/ is a fixture: the go command never builds it as a
# module of this repository, and a lane must not either.
@test "census: a go.mod under testdata/ is not a module of the repository" {
  fixture_repo
  module .
  module tools/sdkguard/testdata/consumer
  run bash scripts/ci/go-modules.sh
  [ "$status" -eq 0 ]
  [ "$output" = "." ]
}

# CI checks out what git tracks, so the census answers for that tree.
@test "census: an untracked go.mod is not reported" {
  fixture_repo
  module .
  mkdir -p scratch
  printf 'module example.com/scratch\n' >scratch/go.mod
  run bash scripts/ci/go-modules.sh
  [ "$status" -eq 0 ]
  [ "$output" = "." ]
}

# A lane looping over nothing passes having built nothing.
@test "census: no module at all is refused, never an empty answer" {
  fixture_repo
  run bash scripts/ci/go-modules.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"empty census"* ]]
}

@test "census: a git that cannot answer is refused" {
  mkdir -p "$WORK/plain/scripts/ci"
  cp "$CI_DIR/go-modules.sh" "$WORK/plain/scripts/ci/"
  cd "$WORK/plain"
  GIT_DIR="$WORK/no-such-git-dir" run bash scripts/ci/go-modules.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"git ls-files failed"* ]]
}

# The repository itself: the two modules #242 found in no 32-bit lane are in
# the census, next to the six the lanes already knew.
@test "census: this repository's census names tools/genindex and tools/sdkguard" {
  run bash "$CI_DIR/go-modules.sh"
  [ "$status" -eq 0 ]
  [[ $'\n'"$output"$'\n' == *$'\ntools/genindex\n'* ]]
  [[ $'\n'"$output"$'\n' == *$'\ntools/sdkguard\n'* ]]
  [[ $'\n'"$output"$'\n' == *$'\n.\n'* ]]
}

# ── the lanes read the census ───────────────────────────────────────────────

# job_body <workflow> <job> — every line of one job, from its key to the next
# job key at the same indent.
job_body() {
  awk -v job="  $2:" '
    $0 == job        { grab = 1; next }
    grab && /^  [A-Za-z0-9_-]+:/ { exit }
    grab             { print }
  ' "$1"
}

# #242 in one assertion: a module-looping lane that carries its own list is how
# tools/ fell out of the 32-bit lane and e2e out of the local audit. Red against
# the workflow as it was, which listed four modules for test-386 and six for
# cross-build.
@test "lanes: cross-build and test-386 loop over the census, not a list" {
  for job in cross-build test-386; do
    body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" "$job")"
    [ -n "$body" ]
    [[ "$body" == *"scripts/ci/go-modules.sh"* ]]
    [[ "$body" != *"for mod in internal/kernel"* ]]
  done
}

@test "lanes: the local cross-platform audit reads the same census" {
  run grep -c 'scripts/ci/go-modules.sh' "$REPO_ROOT/scripts/cross-platform-audit.sh"
  [ "$output" != "0" ]
}

# ── vuln-check.sh ───────────────────────────────────────────────────────────

# stub_scanner — a govulncheck that answers per module: <module>/.vuln-rc holds
# the exit status to give (default 0), and every call is logged.
stub_scanner() {
  cat >"$BATS_TEST_TMPDIR/govulncheck" <<'STUB'
#!/usr/bin/env bash
if [ "${1:-}" = "-version" ]; then
  printf 'Go: go1.27.1\nScanner: govulncheck@%s\n' "${STUB_VERSION:-v1.8.0}"
  exit 0
fi
printf '%s %s\n' "$PWD" "$*" >>"$BATS_TEST_TMPDIR/scans.log"
rc=0
[ -f .vuln-rc ] && rc="$(cat .vuln-rc)"
[ "$rc" -eq 3 ] && echo "Vulnerability #1: GO-2026-0001 — found in example.com/dep@v1.0.0"
exit "$rc"
STUB
  chmod +x "$BATS_TEST_TMPDIR/govulncheck"
  export GOVULNCHECK="$BATS_TEST_TMPDIR/govulncheck"
  : >"$BATS_TEST_TMPDIR/scans.log"
}

scans() {
  local n=0 _l
  while IFS= read -r _l; do n=$((n + 1)); done <"$BATS_TEST_TMPDIR/scans.log"
  echo "$n"
}

@test "vuln: every module of the census is scanned, and a clean tree passes" {
  fixture_repo
  module .
  module a
  module tools/x
  stub_scanner
  run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 0 ]
  [[ "$output" == *"3 module(s) scanned, no reachable vulnerability"* ]]
  [ "$(scans)" -eq 3 ]
}

# The gate: govulncheck exits 3 when a vulnerable symbol is reachable.
@test "vuln: a reachable vulnerability fails, names the module, and the rest are still scanned" {
  fixture_repo
  module .
  module a
  module tools/x
  echo 3 >a/.vuln-rc
  stub_scanner
  run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"vuln-check: a reaches a known-vulnerable symbol"* ]]
  [[ "$output" == *"GO-2026-0001"* ]]
  [ "$(scans)" -eq 3 ]
}

# A scan that did not complete (no network to vuln.go.dev, a module that does
# not load) is not a clean scan, and says so under its own name.
@test "vuln: a scan that did not complete fails, never passes as clean" {
  fixture_repo
  module .
  echo 1 >.vuln-rc
  stub_scanner
  run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"govulncheck did not complete for . (exit 1)"* ]]
}

@test "vuln: a scanner that is not the pinned build is refused" {
  fixture_repo
  module .
  stub_scanner
  STUB_VERSION=v1.7.0 GOVULNCHECK_VERSION=v1.8.0 run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"expected govulncheck@v1.8.0"* ]]
  [ "$(scans)" -eq 0 ]
}

@test "vuln: the pinned build is accepted" {
  fixture_repo
  module .
  stub_scanner
  GOVULNCHECK_VERSION=v1.8.0 run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 0 ]
}

@test "vuln: a missing scanner is refused with the install command" {
  fixture_repo
  module .
  GOVULNCHECK="$BATS_TEST_TMPDIR/no-such-scanner" run -127 bash scripts/ci/vuln-check.sh
  [[ "$output" == *"make vuln-install"* ]]
}

# The gate is `make vuln-check` in the bazel job, the one required check that
# runs code; ci-gates-check.sh asserts the Makefile↔CI link, this asserts the
# job it sits in.
@test "vuln: the scan is a step of the required bazel job" {
  body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" bazel)"
  [[ "$body" == *"make vuln-check"* ]]
}
