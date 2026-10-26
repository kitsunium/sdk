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

# push_block <workflow> — the lines of the `push:` trigger under `on:`, nothing
# when the workflow has none. `push:` alone and `push:` with filters both print
# the header, so a caller can tell "no push trigger" from "an unfiltered one".
push_block() {
  awk '
    /^on:/                       { inon = 1; next }
    inon && /^[^ #]/             { inon = 0 }
    inon && /^  push:/           { grab = 1; print; next }
    grab && /^  [A-Za-z_-]+:/    { grab = 0 }
    grab                         { print }
  ' "$1"
}

# #296: a `pin/*` tag is a marker on a commit, never a build. A workflow whose
# `push` trigger reaches tags ran twice for the two pin tags pushed in W1. A push
# trigger reaches tags when it has no ref filter at all, or a tags filter; it
# must then ignore `pin/**`. And a `tags-ignore` with no `branches` filter would
# stop every BRANCH push — GitHub runs a push event only for the ref kinds a
# filter names once any is named — so the branches are spelled out beside it.
# Red against e2e-cross.yml's bare `push:`.
@test "lanes: no push trigger runs for a pin/* tag, and none stops running for branches" {
  local wf block
  for wf in "$REPO_ROOT"/.github/workflows/*.yml; do
    block="$(push_block "$wf")"
    [ -n "$block" ] || continue
    if [[ "$block" != *"branches"* && "$block" != *"tags"* ]]; then
      echo "$(basename "$wf"): its push trigger has no ref filter, so a pin/* tag runs it" >&2
      return 1
    fi
    if [[ "$block" == *"tags-ignore"* ]]; then
      [[ "$block" == *"'pin/**'"* ]] || { echo "$(basename "$wf"): tags-ignore leaves pin/** in" >&2; return 1; }
      [[ "$block" == *"branches:"* ]] || { echo "$(basename "$wf"): tags-ignore without branches stops every branch push" >&2; return 1; }
    elif [[ "$block" == *"tags:"* ]]; then
      echo "$(basename "$wf"): a tags filter must exclude pin/** — use tags-ignore" >&2
      return 1
    fi
  done
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

# ── ci-gates-check.sh: a gate must block the merge (#236) ───────────────────

# shell-gates is not a required check. Its gates block only because the
# required `bazel` job needs it, checks its result, and is not skipped when it
# fails — `needs:` alone turns a red shell-gates into a SKIPPED bazel, which
# GitHub counts as passing. Each test removes one of the three pieces.
@test "gates: the gates of shell-gates block through the required bazel job" {
  body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" bazel)"
  [[ "$body" == *"    needs: [shell-gates, lint-ktn, models]"* ]]
  [[ "$body" == *'    if: ${{ !cancelled() }}'* ]]
  [[ "$body" == *'needs.shell-gates.result'* ]]
  [[ "$body" == *'needs.lint-ktn.result'* ]]
  [[ "$body" == *'needs.models.result'* ]]
}

@test "gates: a gate in a job the required job does not need is NOT BLOCKING" {
  gates_fixture
  sed -e 's/^    needs: \[shell-gates, lint-ktn, models\]$/    needs: [lint-ktn, models]/' .github/workflows/bazel-ci.yml >wf.tmp
  mv wf.tmp .github/workflows/bazel-ci.yml

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'ci-gates-check' runs only in job 'shell-gates'"* ]]
  [[ "$output" == *"does not list it under needs:"* ]]
}

@test "gates: a needed job whose result the required job ignores is NOT BLOCKING" {
  gates_fixture
  drop_line .github/workflows/bazel-ci.yml 'needs.shell-gates.result'

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'release-scripts-check'"* ]]
  [[ "$output" == *"never checks needs.shell-gates.result"* ]]
}

@test "gates: a result named only in a comment of the required job checks nothing" {
  gates_fixture
  sed -e 's/SHELL_GATES_RESULT: ${{ needs.shell-gates.result }}/SHELL_GATES_RESULT: success  # needs.shell-gates.result/' \
    .github/workflows/bazel-ci.yml >wf.tmp
  sed -e 's/^\( *\)SHELL_GATES_RESULT: success  # \(.*\)$/\1# \2\n\1SHELL_GATES_RESULT: success/' wf.tmp >.github/workflows/bazel-ci.yml

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"never checks needs.shell-gates.result"* ]]
}

@test "gates: a required job skipped when a needed job fails is NOT BLOCKING" {
  gates_fixture
  drop_line .github/workflows/bazel-ci.yml '    if: ${{ !cancelled() }}'

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'ci-gates-check'"* ]]
  [[ "$output" == *"a skipped required check passes"* ]]
}

@test "gates: a guard moved out of the required job is NOT BLOCKING" {
  gates_fixture
  drop_line .github/workflows/bazel-ci.yml "run: bash scripts/pre-commit/check-core-symmetry.sh"
  awk '{ print } /^  docs-site:/ { in_docs = 1 } in_docs && /^    steps:/ { print "      - run: bash scripts/pre-commit/check-core-symmetry.sh"; in_docs = 0 }' \
    .github/workflows/bazel-ci.yml >wf.tmp
  mv wf.tmp .github/workflows/bazel-ci.yml

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'scripts/pre-commit/check-core-symmetry.sh' runs only in job 'docs-site'"* ]]
}

@test "gates: an advisory gate is reported, not failed, and only when listed" {
  gates_fixture

  run bash scripts/ci-gates-check.sh
  [ "$status" -eq 0 ]
  [[ "$output" == *"advisory: 'docs-check' runs in docs-site"* ]]

  drop_line scripts/ci-gates-check.sh "  docs-check"
  # Keep docs-check a gate, drop it from ADVISORY only.
  sed -e 's/^  api-check$/  api-check\n  docs-check/' scripts/ci-gates-check.sh >s.tmp && mv s.tmp scripts/ci-gates-check.sh
  run bash scripts/ci-gates-check.sh
  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'docs-check' runs only in job 'docs-site'"* ]]
}

# ── ktn-linter: a gate, through the public channel (#236) ───────────────────

# lint-ktn-check was kept out of GATES while its step hung on a
# KTN_LINTER_TOKEN secret the repository never had: every run skipped it with a
# warning, and the manifest would have certified a gate that ran nowhere. The
# public `kodflow/ktn/setup` action installs the release binary and proves the
# licence with the runner's OIDC token, so nothing is left to skip it but a
# pull request GitHub gives no id-token to.
@test "ktn: lint-ktn-check is a gate, run by lint-ktn and blocking through bazel" {
  run grep -qxE '  lint-ktn-check' "$REPO_ROOT/scripts/ci-gates-check.sh"
  [ "$status" -eq 0 ]
  body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" lint-ktn)"
  [[ "$body" == *"run: make lint-ktn-check"* ]]
  run bash "$REPO_ROOT/scripts/ci-gates-check.sh"
  [ "$status" -eq 0 ]
  [[ "$output" != *"NOT BLOCKING: 'lint-ktn-check'"* ]]
}

# The three pieces again, for lint-ktn: drop its result check from bazel's
# first step, and its gate stops blocking.
@test "ktn: a lint-ktn result bazel ignores is NOT BLOCKING" {
  gates_fixture
  drop_line .github/workflows/bazel-ci.yml 'needs.lint-ktn.result'

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'lint-ktn-check' runs only in job 'lint-ktn'"* ]]
  [[ "$output" == *"never checks needs.lint-ktn.result"* ]]
}

# ── models: a gate that blocks, and fails without its secret ────────────────

# models-check was advisory while the secret KIT_READ_TOKEN was not set. It is
# a gate now: the required bazel job needs the job and checks its result, and
# the job fails on an empty secret unless the run is a fork's or Dependabot's
# pull request, which GitHub gives no secret.
@test "models: models-check is a gate blocking through bazel, not advisory" {
  run grep -qxE '  models-check' "$REPO_ROOT/scripts/ci-gates-check.sh"
  [ "$status" -eq 0 ]
  run awk '/^ADVISORY=\(/{f=1;next} f&&/^\)/{f=0} f&&/models-check/{found=1} END{exit !found}' "$REPO_ROOT/scripts/ci-gates-check.sh"
  [ "$status" -eq 1 ]
}

@test "models: a models result bazel ignores is NOT BLOCKING" {
  gates_fixture
  drop_line .github/workflows/bazel-ci.yml 'needs.models.result'

  run bash scripts/ci-gates-check.sh

  [ "$status" -eq 1 ]
  [[ "$output" == *"NOT BLOCKING: 'models-check' runs only in job 'models'"* ]]
  [[ "$output" == *"never checks needs.models.result"* ]]
}

@test "models: only a fork or Dependabot pull request skips the check, with a warning" {
  body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" models)"
  [[ "$body" == *'github.event.pull_request.head.repo.full_name'* ]]
  [[ "$body" == *'dependabot[bot]'* ]]
  [[ "$body" == *'::warning title=models gate did not run::'* ]]
  [[ "$body" == *'::error title=models has no KIT_READ_TOKEN::'* ]]
}

# A tag or a branch of a third-party action can be moved under this lane; a
# full commit SHA cannot (.github/workflows/CLAUDE.md, Conventions).
@test "ktn: the setup action is the public one, pinned by a full commit SHA" {
  body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" lint-ktn)"
  run grep -cE 'uses: kodflow/ktn/setup@[0-9a-f]{40}( |$)' <<<"$body"
  [ "$output" = "1" ]
  run grep -cE 'uses: kodflow/ktn/setup@' <<<"$body"
  [ "$output" = "1" ]
}

# The dead path, by its three marks: the secret, the private source fetch, and
# the from-source build CI no longer performs.
@test "ktn: CI needs no secret and fetches no private source" {
  wf="$REPO_ROOT/.github/workflows/bazel-ci.yml"
  run grep -c 'KTN_LINTER_TOKEN' "$wf"
  [ "$output" = "0" ]
  run grep -c 'kodflow/ktn-linter.git' "$wf"
  [ "$output" = "0" ]
  run grep -cE '^[[:space:]]+run: make lint-ktn-install' "$wf"
  [ "$output" = "0" ]
}

# id-token: write lets a job mint a token GitHub signs for this repository, and
# exports ACTIONS_ID_TOKEN_REQUEST_URL to every process of that job. lint-ktn
# needs it for the licence check; the bazel job must NOT hold it, because the
# entitlement suites it runs read that variable and take the CI path — which
# is what #300's first run measured. The workflow root stays at contents: read.
@test "ktn: id-token is granted to lint-ktn alone, never to bazel or the workflow" {
  wf="$REPO_ROOT/.github/workflows/bazel-ci.yml"
  root_perms="$(awk '/^permissions:/ { p = 1; next } p && /^[^[:space:]#]/ { exit } p { print }' "$wf")"
  [[ "$root_perms" == *"contents: read"* ]]
  [[ "$root_perms" != *"id-token"* ]]
  body="$(job_body "$wf" lint-ktn)"
  [[ "$body" == *"      id-token: write"* ]]
  [[ "$body" == *"      contents: read"* ]]
  bazel_body="$(job_body "$wf" bazel)"
  run grep -cE '^[[:space:]]+id-token:' <<<"$bazel_body"
  [ "$output" = "0" ]
  # As a permissions KEY, exactly once in the file: no other job asks for it.
  run grep -cE '^[[:space:]]+id-token:[[:space:]]*write[[:space:]]*$' "$wf"
  [ "$output" = "1" ]
}

# A gate that can go red and still let the job pass is not a gate.
@test "ktn: no step of lint-ktn or of the required job continues on error" {
  for job in lint-ktn bazel; do
    body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" "$job")"
    run grep -c 'continue-on-error' <<<"$body"
    [ "$output" = "0" ]
  done
}

# The one skip GitHub forces: a fork or Dependabot pull request gets no
# id-token. It must say so, and a run that is neither must not skip quietly
# when the token is missing.
@test "ktn: only a fork or Dependabot pull request skips the linter, with a warning" {
  body="$(job_body "$REPO_ROOT/.github/workflows/bazel-ci.yml" lint-ktn)"
  [[ "$body" == *'github.event.pull_request.head.repo.full_name'* ]]
  [[ "$body" == *'dependabot[bot]'* ]]
  [[ "$body" == *'::warning title=ktn-linter gate did not run::'* ]]
  [[ "$body" == *'ACTIONS_ID_TOKEN_REQUEST_URL'* ]]
  run grep -c "if: steps.ktnoidc.outputs.run == 'true'" <<<"$body"
  [ "$output" = "2" ]
}

# ── run-test-binaries.sh: what every guest runs ─────────────────────────────

# binaries_fixture — a repository root holding two package directories and a
# BINDIR whose manifest names a "test binary" for each: a shell script that
# records the directory it ran in and its arguments, then exits with the code
# its marker file holds (0 when there is none).
binaries_fixture() {
  cd "$WORK"
  mkdir -p a/pkg b/pkg bins
  for n in a b; do
    cat >"bins/$n.test" <<SH
#!/bin/sh
pwd >>"$WORK/$n.ran"
echo "\$*" >>"$WORK/$n.args"
if [ -f "$WORK/$n.hang" ]; then
  echo "panic: test timed out after 5m0s"
  exit 2
fi
c=0
if [ -f "$WORK/$n.codes" ]; then
  c=\$(head -n 1 "$WORK/$n.codes")
  sed -i.bak 1d "$WORK/$n.codes"
  [ -n "\$c" ] || c=0
fi
exit "\$c"
SH
    chmod +x "bins/$n.test"
  done
  printf 'a.test a/pkg\nb.test b/pkg\n' >bins/manifest
}

# A suite reads its testdata/ by relative path, so each binary runs from its
# own package directory, as `go test` runs it; the flags reach every binary.
@test "binaries: each runs from its package directory, with the flags given" {
  binaries_fixture

  run sh "$CI_DIR/run-test-binaries.sh" bins -test.v

  [ "$status" -eq 0 ]
  [ "$(cat "$WORK/a.ran")" = "$WORK/a/pkg" ]
  [ "$(cat "$WORK/b.ran")" = "$WORK/b/pkg" ]
  [[ "$(cat "$WORK/a.args")" == *"-test.timeout=5m -test.v"* ]]
  [[ "$output" == *"2 suites on"* ]]
}

# A real defect fails every attempt: the run fails and names the package, and
# the suites after it still run.
@test "binaries: a suite failing every attempt fails the run, named, and the rest still run" {
  binaries_fixture
  printf '1\n1\n1\n' >"$WORK/a.codes"

  run sh "$CI_DIR/run-test-binaries.sh" bins

  [ "$status" -eq 1 ]
  [[ "$output" == *"::error::a/pkg fails on"* ]]
  [ "$(wc -l <"$WORK/a.ran" | tr -d ' ')" = "3" ]
  [ -f "$WORK/b.ran" ]
}

# A retry that passes is not a pass: the package is named as flaky and the
# run FAILS, so a flake is detected and never masked; the suites after it
# still run.
@test "binaries: a suite passing only on a retry fails the run, named as flaky" {
  binaries_fixture
  printf '1\n0\n' >"$WORK/a.codes"

  run sh "$CI_DIR/run-test-binaries.sh" bins

  [ "$status" -eq 1 ]
  [[ "$output" == *"::error::a/pkg is flaky on"*"passed on attempt 2"* ]]
  [[ "$output" == *"FLAKY a/pkg"* ]]
  [[ "$output" != *"::error::a/pkg fails on"* ]]
  [[ "$output" != *"::warning::"* ]]
  [ "$(wc -l <"$WORK/a.ran" | tr -d ' ')" = "2" ]
  [ -f "$WORK/b.ran" ]
}

# A suite that passes at once is the only green: nothing is named.
@test "binaries: suites passing on their first attempt pass, and nothing is named" {
  binaries_fixture

  run sh "$CI_DIR/run-test-binaries.sh" bins

  [ "$status" -eq 0 ]
  [[ "$output" != *"::error::"* ]]
  [[ "$output" != *"FLAKY"* ]]
  [ "$(wc -l <"$WORK/a.ran" | tr -d ' ')" = "1" ]
}

# A hang spends one timeout, never three: retried, it would eat the guest
# step's cap and leave the suites after it unrun and the summary unprinted.
@test "binaries: a suite that timed out is not tried again, and fails named" {
  binaries_fixture
  touch "$WORK/a.hang"

  run sh "$CI_DIR/run-test-binaries.sh" bins

  [ "$status" -eq 1 ]
  [ "$(wc -l <"$WORK/a.ran" | tr -d ' ')" = "1" ]
  [[ "$output" == *"timed out after 5m"* ]]
  [[ "$output" == *"::error::a/pkg fails on"* ]]
  [ -f "$WORK/b.ran" ]
}

# A run that executed nothing must not pass.
@test "binaries: an empty or missing manifest is refused" {
  binaries_fixture
  : >bins/manifest
  run sh "$CI_DIR/run-test-binaries.sh" bins
  [ "$status" -eq 1 ]
  rm bins/manifest
  run sh "$CI_DIR/run-test-binaries.sh" bins
  [ "$status" -eq 1 ]
}

# The guests run EVERY package, compiled from the census by test-binaries.sh,
# not a list of "platform-sensitive" packages that left 300 of 333 unexecuted;
# and no job of the runtime lane shrinks a suite with -short (rule 12).
@test "lanes: every guest job runs the census's test binaries, and nothing runs -short" {
  local wf="$REPO_ROOT/.github/workflows/e2e-cross.yml"
  for job in bsd solarish; do
    body="$(job_body "$wf" "$job")"
    [ -n "$body" ]
    [[ "$body" == *"bash scripts/ci/test-binaries.sh bins"* ]]
    [[ "$body" == *"sh scripts/ci/run-test-binaries.sh bins"* ]]
    [[ "$body" != *'$SERVICE_PKGS'* ]]
  done
  # Comments name -short to say why it is gone; only the commands count.
  run bash -c "grep -v '^[[:space:]]*#' '$wf' | grep -cE -- '-(test[.])?short'"
  [ "$output" = "0" ]
}

# A retry in the runtime lane only names a failure. Every job that tries a
# suite or the conformance program again fails a pass on a retry as flaky,
# and none says a retry passed with a mere warning: CI detects a flake, it
# never masks one.
@test "lanes: no runtime job lets a test pass on a retry" {
  local wf="$REPO_ROOT/.github/workflows/e2e-cross.yml"
  for job in linux-init bsd solarish; do
    body="$(job_body "$wf" "$job")"
    [ -n "$body" ]
    [[ "$body" == *"is flaky on"* ]]
    [[ "$body" != *"passed only on a retry"* ]]
    [[ "$body" != *'"$@" && return 0'* ]]
  done
  run grep -c '::warning::.*retry' "$wf" "$CI_DIR/run-test-binaries.sh"
  [[ "$output" != *":"[1-9]* ]]
}

# The summary step runs on every run, including the one where the cut step was
# skipped and /tmp/cut.err, /tmp/cut.log never existed. Under the runner's
# default `bash -e` (which `set -uo pipefail` does not undo), an awk on the
# missing file exits 2 and failed a run whose verdict, "nothing consumer-visible
# changed", was right (run 39450668933). The block is run as the runner runs
# it, with its /tmp redirected, in each rendering that reads those files.
@test "release: the summary step cannot fail when the cut step was skipped" {
  local wf="$REPO_ROOT/.github/workflows/sdk-release.yml"
  local script="$BATS_TEST_TMPDIR/summary.sh"
  awk '
    /- name: Emit release summary artifact/ { grab = 1; next }
    grab && /^      - name:/ { exit }
    grab && /^        run: \|/ { run = 1; next }
    grab && run { sub(/^          /, ""); print }
  ' "$wf" | sed "s#/tmp/#$BATS_TEST_TMPDIR/#g" > "$script"
  [ -s "$script" ]
  export TARGET_SHA=0123456 GITHUB_STEP_SUMMARY="$BATS_TEST_TMPDIR/step.md"
  # Nothing computed, then a dry run, then a held bootstrap, then a real cut:
  # none has a cut.err, and none may fail.
  for env in "MAJORS= DRY_RUN=" "MAJORS=. DRY_RUN=true" "MAJORS=. HELD=true" "MAJORS=. DRY_RUN=false"; do
    rm -f "$BATS_TEST_TMPDIR"/cut.err "$BATS_TEST_TMPDIR"/cut.log
    run env $env CUT_OUTCOME=skipped bash -e "$script"
    [ "$status" -eq 0 ]
  done
  [[ "$(cat "$BATS_TEST_TMPDIR/release-summary.md")" == *"SDK release summary"* ]]
}

# ── check-net-default-change.sh (ADR 0188) ──────────────────────────────────

# body <text> — a pull request body in a file, printf-expanded.
net_body() {
  printf "$1" >"$BATS_TEST_TMPDIR/body.md"
}

@test "net-default-change: an Override block in the registry's tokens passes" {
  net_body 'Turns multishot on.\r\n\r\nOverride:\r\n  KITSUNIUM_NET_DISABLE=iouring, epoll\r\nKITSUNIUM_NET_ENGINE=portable\r\n\r\nmore text\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 0 ]
  [[ "$output" == *"matches the registry"* ]]
}

@test "net-default-change: a block inside a fence ends at the fence" {
  net_body '```\nOverride:\nKITSUNIUM_NET_ENGINE=epoll\n```\nKITSUNIUM_NET_ENGINE=bogus\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 0 ]
}

@test "net-default-change: no block is refused, naming what to add" {
  net_body 'Changes a default and says nothing.\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 1 ]
  [[ "$output" == *"holds no Override: block"* ]]
}

@test "net-default-change: an unknown token, a floor switched off and a foreign variable are each refused" {
  net_body 'Override:\nKITSUNIUM_NET_ENGINE=kqueue\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 1 ]
  [[ "$output" == *"KITSUNIUM_NET_ENGINE=kqueue names no engine"* ]]
  net_body 'Override:\nKITSUNIUM_NET_DISABLE=portable\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 1 ]
  [[ "$output" == *"'portable' is no capability an operator may switch off (iouring, epoll, uring-multishot-recv,"* ]]
  net_body 'Override:\nGODEBUG=x\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 1 ]
  [[ "$output" == *"'GODEBUG' is not one of the operator's variables"* ]]
  net_body 'Override:\nKITSUNIUM_NET_ENGINE=epoll\nKITSUNIUM_NET_ENGINE=portable\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 1 ]
  [[ "$output" == *"at most once"* ]]
}

@test "net-default-change: a kernel capability is switched off, never named as an engine (ADR 0189)" {
  net_body 'Override:\nKITSUNIUM_NET_DISABLE=uring-direct,tcp-nodelay\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 0 ]
  net_body 'Override:\nKITSUNIUM_NET_ENGINE=uring-pbuf\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 1 ]
  [[ "$output" == *"names no engine of the registry (iouring, epoll, portable, goroutine)"* ]]
}

@test "net-default-change: the registry is the design's, so a token it drops is refused" {
  sed 's/"\\"epoll\\""/"\\"renamed\\""/' "$REPO_ROOT/design/net.yaml" >"$BATS_TEST_TMPDIR/net.yaml"
  net_body 'Override:\nKITSUNIUM_NET_DISABLE=epoll\n'
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md" "$BATS_TEST_TMPDIR/net.yaml"
  [ "$status" -eq 1 ]
  run bash "$CI_DIR/check-net-default-change.sh" "$BATS_TEST_TMPDIR/body.md"
  [ "$status" -eq 0 ]
}

@test "net-default-change: the workflow runs on the pull request and in the merge queue, read-only" {
  wf="$REPO_ROOT/.github/workflows/net-default-change.yml"
  grep -q '^  merge_group:' "$wf"
  grep -q 'types: \[opened, edited, reopened, synchronize, labeled, unlabeled\]' "$wf"
  grep -q 'scripts/ci/check-net-default-change.sh' "$wf"
  run grep -nE '^  pull_request_target:|: write' "$wf"
  [ "$status" -ne 0 ]
}
