#!/usr/bin/env bats
# BATS tests for scripts/ci/: the module census every module-looping lane reads
# (go-modules.sh, ADR 0137), the platforms table (platforms.sh) and the
# govulncheck gate built on the census (vuln-check.sh, ADR 0136) — and for
# scripts/ci-gates-check.sh: the guards it keeps steps of the bazel job, and a
# gate CI runs through a listed gate's recipe. Run by `make ci-scripts-check`
# in the shell-gates job.
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

# ADR 0162: the root is the SDK module, so no census loop skips it. The skip
# ADR 0157 §5 allowed while the root held no package — `[ "$mod" = "." ]` and
# a `go list` that came back empty — must not survive the root's packages:
# kept, it would skip the SDK the day `go list` fails.
@test "lanes: no census loop skips the root module" {
  for job in cross-build test-386; do
    body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" "$job")"
    [ -n "$body" ]
    [[ "$body" != *'[ "$mod" = "." ]'* ]]
  done
  run grep -c '"$mod" = "."' "$REPO_ROOT/.github/workflows/e2e-cross.yml"
  [ "$output" = "0" ]
}

@test "lanes: the local cross-platform audit reads the same census" {
  run grep -c 'scripts/ci/go-modules.sh' "$REPO_ROOT/scripts/cross-platform-audit.sh"
  [ "$output" != "0" ]
}

# ── platforms.sh ────────────────────────────────────────────────────────────

# The cells are one table: the script prints it, genindex reads its
# here-document, and scripts/pre-commit/check-platforms.sh holds it to the
# cross-build matrix. illumos and solaris are two cells (ADR 0144).
@test "platforms: the table prints the twelve cells, illumos and solaris apart" {
  run bash "$CI_DIR/platforms.sh"

  [ "$status" -eq 0 ]
  n=0
  while IFS= read -r cell; do
    [[ "$cell" =~ ^[a-z0-9]+/[a-z0-9]+$ ]]
    n=$((n + 1))
  done <<<"$output"
  [ "$n" -eq 12 ]
  [[ "$output" == *$'illumos/amd64\nsolaris/amd64'* ]]
}

@test "lanes: the local cross-platform audit reads the platforms table, not a list" {
  run grep -c 'scripts/ci/platforms.sh' "$REPO_ROOT/scripts/cross-platform-audit.sh"
  [ "$output" != "0" ]
  run grep -c 'linux/amd64' "$REPO_ROOT/scripts/cross-platform-audit.sh"
  [ "$output" = "0" ]
}

# ── vuln-check.sh ───────────────────────────────────────────────────────────

# stub_scanner — a govulncheck that answers per module: <module>/.vuln-rc holds
# the exit status to give (default 0), <module>/.vuln-msg a line to print, and
# every call is logged.
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
[ -f .vuln-msg ] && cat .vuln-msg
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

# ADR 0162: the root is the SDK module — internal/, pkg/, framework/ — and no
# longer the empty anchor ADR 0157 §5 excused. "No packages" from it is the SDK
# module having lost its packages: a scan that did not complete, as for any
# other module. Red against the script as it was, which passed it.
@test "vuln: the root module holding no package fails, as any module does (ADR 0162)" {
  fixture_repo
  module .
  module a
  echo 2 >.vuln-rc
  echo "govulncheck: no packages matched the provided patterns" >.vuln-msg
  stub_scanner
  run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"govulncheck did not complete for . (exit 2)"* ]]
  [ "$(scans)" -eq 2 ]
}

@test "vuln: any other module holding no package fails as incomplete" {
  fixture_repo
  module .
  module a
  echo 2 >a/.vuln-rc
  echo "govulncheck: no packages matched the provided patterns" >a/.vuln-msg
  stub_scanner
  run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"govulncheck did not complete for a (exit 2)"* ]]
}

# Any other exit 2 from the root is incomplete too.
@test "vuln: the root failing for another reason still fails" {
  fixture_repo
  module .
  echo 2 >.vuln-rc
  echo "govulncheck: loading packages: there are errors" >.vuln-msg
  stub_scanner
  run bash scripts/ci/vuln-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"govulncheck did not complete for . (exit 2)"* ]]
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

# ── ci-gates-check.sh: the guards ───────────────────────────────────────────

# gates_fixture — a repository holding this repository's Makefile, workflow and
# ci-gates-check.sh, with every listed guard present as a file, so each test
# below changes ONE thing and the script answers for that change alone.
gates_fixture() {
  cd "$WORK"
  mkdir -p scripts/pre-commit .github/workflows
  cp "$REPO_ROOT/scripts/ci-gates-check.sh" scripts/
  cp "$REPO_ROOT/Makefile" Makefile
  cp "$REPO_ROOT/.github/workflows/bazel-ci.yml" .github/workflows/
  for guard in "$REPO_ROOT"/scripts/pre-commit/check-*.sh "$REPO_ROOT"/scripts/check-layer-deps.sh; do
    cp "$guard" "${guard#"$REPO_ROOT"/}"
  done
}

# drop_line <file> <fixed string> — remove every line holding the string.
drop_line() {
  grep -vF -- "$2" "$1" >"$1.tmp" || true
  mv "$1.tmp" "$1"
}

@test "gates: this repository's gates and guards are all enforced" {
  run bash "$REPO_ROOT/scripts/ci-gates-check.sh"
  [ "$status" -eq 0 ]
  [[ "$output" == *"guards are enforced"* ]]
}

# The shape the header calls silent: the step goes, the guard survives in
# `make lint`, and CI stops running it. Its name in the step's comment and
# title must not count.
@test "gates: a guard whose CI step was deleted is UNGATED" {
  gates_fixture
  drop_line .github/workflows/bazel-ci.yml "run: bash scripts/pre-commit/check-core-symmetry.sh"

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"UNGATED: 'scripts/pre-commit/check-core-symmetry.sh'"* ]]
}

@test "gates: a guard make lint no longer runs is NOT LINTED" {
  gates_fixture
  drop_line Makefile "	bash scripts/pre-commit/check-core-symmetry.sh"

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT LINTED: 'scripts/pre-commit/check-core-symmetry.sh'"* ]]
}

# api-check has no step of its own: CI runs it through `make lint-check`,
# whose recipe runs `$(MAKE) api-check`. That line is then the gate's only
# link to CI, and deleting it must read as the silent deletion it is.
@test "gates: a gate a listed gate's recipe runs is enforced through it" {
  gates_fixture

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 0 ]
  run grep -c 'run: make api-check' .github/workflows/bazel-ci.yml
  [ "$output" = "0" ]
}

@test "gates: a gate whose caller's recipe no longer runs it is UNGATED" {
  gates_fixture
  drop_line Makefile "MAKE) --no-print-directory api-check"

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"UNGATED: 'make api-check'"* ]]
}

@test "gates: a listed guard that does not exist is MISSING" {
  gates_fixture
  rm scripts/pre-commit/check-core-symmetry.sh

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"MISSING GUARD: 'scripts/pre-commit/check-core-symmetry.sh'"* ]]
}
