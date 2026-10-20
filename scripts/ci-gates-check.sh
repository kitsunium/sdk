#!/usr/bin/env bash
#
# ci-gates-check.sh — fail when a quality gate stops being a quality gate.
#
# A Makefile target CI never runs is documentation, not a gate. This checks the
# two ways that happens, both of them silent:
#
#   1. the target is removed or renamed, and the CI step now invokes nothing;
#   2. the CI step is deleted, and the target survives as a thing people are
#      told to run by hand.
#
# The SDK had a live specimen of the second shape before this script existed,
# and it is what motivated it: scripts/release/*.bats — the regression suite
# for the two scripts that decide which tag every merge gets — was executed by
# no make target, no CI lane and no hook, from the commit that introduced it.
# ADR 0085 §Deferred records the gap; nothing enforced closing it.
#
# The manifest below includes this script's own target, but be precise about what
# that buys. Deleting ANOTHER gate's step is caught: this script still runs and
# reports it UNGATED. Renaming or deleting a gate's TARGET is caught: the step
# still runs and make fails on it. Deleting THIS script's own step is NOT caught
# by this script — nothing that has been removed can report its own removal.
# That last case is branch protection's job, and as measured when this was
# written `main` requires only the `bazel` check, so it is currently nobody's.
# Recorded under ADR 0088 Deferred rather than overstated here.
#
# A third way a gate stops being one is that it runs and blocks nothing: a step
# in a job no required check depends on reports red and the merge goes ahead
# (#236). So every gate and guard must run in REQUIRED_JOB, or in a job that
# REQUIRED_JOB `needs:`, whose result it checks, and that it does not skip on
# failure — `needs:` alone makes a failure SKIP the required job, and a skipped
# required check counts as passing. A gate that is deliberately advisory is
# listed in ADVISORY with the reason, and reported as such instead of failing.
#
# Scope: this asserts the Makefile↔CI link for the targets in GATES, and the
# same link for the guards in GUARDS — the shell checks `make lint` runs and
# the `bazel` job invokes directly as `bash scripts/…` steps. A renamed guard
# fails loudly by itself (exit 127), but a DELETED step is the second shape
# above, silent: the guard survives in `make lint` and gates nothing, which is
# what this script exists to catch, so the guards are listed too. The measured
# asymmetries that are NOT covered here are written down in ADR 0088 rather
# than silently enforced.

set -euo pipefail

cd "$(dirname "$0")/.."

# The SDK lane is bazel-ci.yml, not ci.yml: .github/workflows/CLAUDE.md makes it
# the single source of truth for gating (ADR 0004), and the other workflows are
# release-triggered, scheduled or path-gated scans and suites that gate nothing.
WORKFLOW=".github/workflows/bazel-ci.yml"
MAKEFILE="Makefile"

# Targets that MUST exist in the Makefile and MUST be invoked by the CI lane.
# Add a gate here the moment it becomes one; the point is that removing it from
# CI has to be a visible edit to this list.
GATES=(
  ci-gates-check
  test-framework
  release-scripts-check
  pre-commit-check
  # `gofumpt -l` + sdkguard: two of the three checks of `make lint` that no
  # lane ran until #236. The other five are invoked by the workflow as direct
  # `bash …` steps, so they are outside this list by construction.
  lint-check
  # ktn-linter, the third. It was kept out of this list while its step hung on
  # a repository secret that never existed, so every run skipped it: a step a
  # missing secret can skip is not a gate. The binary now comes from the
  # vendor's public channel (`kodflow/ktn/setup`) and proves its licence with
  # the runner's OIDC token, so the only runs that skip it are a fork's or
  # Dependabot's pull request, which GitHub gives no id-token — with a warning,
  # and the step fails on any other run that has none (#236).
  lint-ktn-check
  # scripts/ci/*.bats — the module census and the govulncheck gate (ADR 0136,
  # ADR 0137). A suite nothing runs is not a test suite (ADR 0088).
  ci-scripts-check
  # govulncheck over every module, in the required `bazel` job (#210, ADR
  # 0136). `vuln-install` is its installer, not a gate, and is not listed.
  vuln-check
  # docs/api regenerated from the code on every cell and compared byte for
  # byte. CI runs it through `make lint-check`, whose recipe runs
  # `$(MAKE) api-check` — enforced through a listed gate's recipe, below.
  api-check
  # The docs portal built from docs/api: its ⌘K index and API sections equal
  # docs/api, and every link resolves — the `docs-site` job, a sibling of the
  # required ones.
  docs-check
  # The behaviour models held to the code by the pinned kit — the `models`
  # job, a sibling of the required ones (kitsunium/studio ADR 0013).
  models-check
)

# The job branch protection requires, by its check name. Measured with
# `gh api repos/kitsunium/sdk/branches/main/protection` (contexts: ["bazel"]);
# the ruleset requires `post-commit`, a workflow that runs no gate of this file.
REQUIRED_JOB="bazel"

# Gates CI runs in a job that blocks nothing, on purpose. Each is reported, not
# failed; anything else outside a blocking job fails as NOT BLOCKING.
ADVISORY=(
  # docs-site: the portal build reads docs/api, which `make api-check` holds to
  # the code inside the required job; the deploy runs the same check over every
  # release before it publishes.
  docs-check
  # models: kit lives in a private repository, installed with the secret
  # KIT_READ_TOKEN; without it the job warns and passes, and a step a missing
  # secret can skip is not a gate (#236's rule for lint-ktn-check). `kit
  # check`'s behaviour rule holds the same models locally.
  models-check
)

# Guards that `make lint` runs AND the `bazel` job invokes as a direct
# `bash <path>` step: each must exist, be a step of the workflow and a line of
# the Makefile's `lint` recipe. Add a guard here in the commit that wires it.
GUARDS=(
  scripts/pre-commit/check-alloc-lane-coverage.sh
  scripts/pre-commit/check-audit-coverage.sh
  scripts/pre-commit/check-domain-docs.sh
  # ADR 0160: no service declares a code; the core mirrors the service.
  scripts/pre-commit/check-core-symmetry.sh
  scripts/pre-commit/check-pkg-docs.sh
  scripts/pre-commit/check-bench-md.sh
  scripts/pre-commit/check-error-codes-drift.sh
  # Every README is what docs/api writes (ADR 0167): genindex, no binary.
  scripts/pre-commit/check-readme-drift.sh
  # The platforms table genindex reads is the cross-build matrix CI compiles.
  scripts/pre-commit/check-platforms.sh
  scripts/check-layer-deps.sh
)

fail=0

note() { printf '  %s\n' "$1"; }

# Every .PHONY declaration in the Makefile, backslash continuations folded in,
# flattened to a single space-separated list. Compared whole-word afterwards, so
# `release-scripts-check` can never be satisfied by a `release-scripts-check-v2`
# that happens to contain it.
phony_targets=""

if [ ! -f "$WORKFLOW" ]; then
  echo "ci-gates-check: $WORKFLOW not found"
  exit 1
fi

if [ ! -f "$MAKEFILE" ]; then
  echo "ci-gates-check: $MAKEFILE not found"
  exit 1
fi

phony_targets="$(
  awk '
    /^\.PHONY:/ { collecting = 1; sub(/^\.PHONY:/, ""); }
    collecting {
      line = $0
      cont = (line ~ /\\$/)
      sub(/\\$/, "", line)
      printf "%s ", line
      if (!cont) { collecting = 0 }
    }
  ' "$MAKEFILE" | tr -s '[:space:]' ' '
)"

if [ -z "${phony_targets// /}" ]; then
  echo "ci-gates-check: $MAKEFILE declares no .PHONY targets — has the layout changed?"
  exit 1
fi

# Every command the workflow actually EXECUTES: the value of each `run:` key,
# plus the body of each `run: |` block scalar, with comment-only lines dropped.
# Anything outside a run: — step names, `name:` prose, the long justification
# comments this repository writes above each gate — is not a command and must
# not be able to satisfy enforcement.
# Each line is `<job id><TAB><command>`; the job is the two-space key under
# `jobs:` the run: belongs to.
ci_job_commands="$(
  awk '
    /^jobs:/ { in_jobs = 1; next }
    /^[^[:space:]#]/ { in_jobs = 0 }
    in_jobs && /^  [A-Za-z0-9_-]+:[[:space:]]*$/ {
      job = $0
      sub(/^  /, "", job)
      sub(/:.*/, "", job)
      in_run = 0
      next
    }
    {
      line = $0
      indent = match(line, /[^ ]/) - 1
      if (indent < 0) { indent = 0 }

      if (in_run) {
        if (line ~ /^[[:space:]]*$/) { next }
        if (indent > run_indent) {
          body = line
          sub(/^[[:space:]]+/, "", body)
          if (body !~ /^#/) { print job "\t" body }
          next
        }
        in_run = 0
      }

      if (line ~ /^[[:space:]]*(- )?run:/) {
        value = line
        sub(/^[[:space:]]*(- )?run:[[:space:]]*/, "", value)
        run_indent = indent
        in_run = 1
        # `run: |` and `run: >` carry the command in the block below, not here.
        if (value != "" && value !~ /^[|>]/) { print job "\t" value }
      }
    }
  ' "$WORKFLOW"
)"
ci_run_commands="$(cut -f2- <<<"$ci_job_commands")"

if [ -z "${ci_run_commands//[[:space:]]/}" ]; then
  echo "ci-gates-check: $WORKFLOW declares no run: commands — has the layout changed?"
  exit 1
fi

# job_body <job> — the lines of one job of the workflow, its key excluded.
job_body() {
  awk -v job="  $1:" '
    $0 == job { grab = 1; next }
    grab && /^  [A-Za-z0-9_-]+:/ { exit }
    grab && /^[^[:space:]#]/ { exit }
    grab { print }
  ' "$WORKFLOW"
}

required_body="$(job_body "$REQUIRED_JOB")"
if [ -z "${required_body//[[:space:]]/}" ]; then
  echo "ci-gates-check: $WORKFLOW has no '$REQUIRED_JOB' job — the required check runs nothing"
  exit 1
fi

# The jobs REQUIRED_JOB names in its job-level `needs:`, one per line — the
# scalar form and the flow-sequence form; a block sequence is refused below
# rather than misread as none.
required_needs="$(
  awk '
    /^    needs:/ {
      v = $0
      sub(/^    needs:[[:space:]]*/, "", v)
      gsub(/[][,]/, " ", v)
      n = split(v, a, /[[:space:]]+/)
      for (i = 1; i <= n; i++) if (a[i] != "") print a[i]
    }
  ' <<<"$required_body"
)"
required_if="$(grep -E '^    if:' <<<"$required_body" || true)"
# Its commands and keys without comment lines: a comment naming
# needs.<job>.result checks nothing.
required_code="$(grep -vE '^[[:space:]]*#' <<<"$required_body" || true)"

# blocking <job> — whether a red step in <job> fails REQUIRED_JOB's check.
blocking() {
  [ "$1" = "$REQUIRED_JOB" ] && return 0
  grep -qxF -- "$1" <<<"$required_needs" || return 1
  grep -qF -- "needs.$1.result" <<<"$required_code" || return 1
  grep -qE '!cancelled\(\)|always\(\)' <<<"$required_if" || return 1
}

# why_not_blocking <job> — the missing piece, for the message.
why_not_blocking() {
  if ! grep -qxF -- "$1" <<<"$required_needs"; then
    echo "'$REQUIRED_JOB' does not list it under needs:"
  elif ! grep -qF -- "needs.$1.result" <<<"$required_code"; then
    echo "'$REQUIRED_JOB' never checks needs.$1.result, so its failure is ignored"
  else
    echo "'$REQUIRED_JOB' has no job-level if: !cancelled() or always(), so a failure of $1 SKIPS it, and a skipped required check passes"
  fi
}

# jobs_running <regex> — every job holding a run: command that matches.
jobs_running() {
  awk -F'\t' -v re="$1" '$2 ~ re { print $1 }' <<<"$ci_job_commands" | sort -u
}

# check_blocking <label> <regex> — fail unless at least one job running the
# command blocks the merge; an ADVISORY gate is reported instead.
check_blocking() {
  local label="$1" re="$2" job jobs advisory=0 a
  jobs="$(jobs_running "$re")"
  [ -n "$jobs" ] || return 0
  while IFS= read -r job; do
    blocking "$job" && return 0
  done <<<"$jobs"
  for a in "${ADVISORY[@]}"; do [ "$a" = "$label" ] && advisory=1; done
  if [ "$advisory" -eq 1 ]; then
    note "advisory: '$label' runs in $(tr '\n' ' ' <<<"$jobs")— a job no required check depends on, as ADVISORY declares"
    return 0
  fi
  job="$(head -n1 <<<"$jobs")"
  echo "NOT BLOCKING: '$label' runs only in job '$job', and a failure there does not fail the required '$REQUIRED_JOB' check: $(why_not_blocking "$job")"
  note "make '$REQUIRED_JOB' need the job, check its result and run when it fails — or list '$label' in ADVISORY in $0, saying why"
  fail=1
}

# recipe <target> — the commands of one Makefile target's recipe: its
# tab-indented lines up to the next rule, comment-only lines dropped, as for
# `lint` below.
recipe() {
  awk -v target="$1:" '
    index($0, target) == 1 { in_recipe = 1; next }
    in_recipe && /^[^\t#[:space:]]/ { exit }
    in_recipe && /^\t/ {
      body = $0
      sub(/^\t+/, "", body)
      if (body !~ /^#/) { print body }
    }
  ' "$MAKEFILE"
}

# runs_directly <gate> — whether a run: command of the workflow is `make <gate>`
# at a command position.
runs_directly() {
  grep -qE "(^|[;&|(][[:space:]]*)make ${1}([[:space:]]|$)" <<<"$ci_run_commands"
}

# runs_through <gate> — the listed gate the workflow runs whose recipe runs
# `$(MAKE) <gate>` at a command position, or nothing. One level deep: a gate
# CI reaches through a gate CI reaches is enforced; a target that only another
# unlisted target calls is not.
runs_through() {
  local caller
  for caller in "${GATES[@]}"; do
    [ "$caller" = "$1" ] && continue
    runs_directly "$caller" || continue
    if grep -qE '(^|[;&|(][[:space:]]*)\$\(MAKE\)( --no-print-directory)? '"${1}"'([[:space:]]|$)' <<<"$(recipe "$caller")"; then
      echo "$caller"
      return 0
    fi
  done
  return 0
}

for gate in "${GATES[@]}"; do
  if ! grep -qE "^${gate}:" "$MAKEFILE"; then
    echo "MISSING TARGET: '${gate}' is listed as a CI gate but no such target exists in $MAKEFILE"
    note "either restore the target or remove it from GATES in $0"
    fail=1
    continue
  fi

  # A target that is not .PHONY is one `touch ci-gates-check` away from being
  # skipped as up to date. These gates produce no file, so the declaration is
  # the only thing that keeps them runnable.
  #
  # Matched against the JOINED declaration, not the raw line: .PHONY is one long
  # line today, and the obvious tidy-up is to wrap it with backslashes the way
  # ktn-linter's is wrapped. A line-anchored grep would then report every gate as
  # NOT PHONY — a false red on a reformat, which is how a gate loses its
  # credibility. `phony_targets` is computed once, above the loop.
  #
  # A `case` rather than `printf … | grep -qF`: that pipeline is the construct
  # that classified 19 `feat:` commits as patches in the sibling repository —
  # `grep -q` closes the pipe on its first match, the writer takes EPIPE, and
  # `pipefail` reports 141 for input that DID match. It needs an input past the
  # pipe buffer to bite, which a .PHONY line is not; writing it anyway would be
  # betting on a size. `case` is a shell builtin: no pipe, no subprocess, no bet.
  case " $phony_targets " in
    *" $gate "*) ;;
    *)
      echo "NOT PHONY: '${gate}' is a CI gate but is not declared in .PHONY in $MAKEFILE"
      note "a file of that name in the worktree would make make skip it"
      fail=1
      ;;
  esac

  # Matched against the EXECUTABLE `run:` commands only, never the raw file.
  # Grepping the whole YAML accepts a gate that exists solely in a comment: the
  # step gets deleted, the explanatory comment above it survives naming the
  # command, and the check stays green over a gate that no longer runs. That is
  # the exact failure this script exists to catch, so it must not be the way the
  # script itself fails.
  #
  # It must also be at a COMMAND position — start of the command, or after a
  # `;`/`&&`/`||`/`|`/`(`. Otherwise `run: echo "we used to run make <gate>"`
  # satisfies enforcement with an echo.
  #
  # A gate CI runs through another listed gate's recipe — `lint-check` runs
  # `$(MAKE) api-check` — is run as surely as one with its own step, and a
  # second step would run it twice. Deleting the recipe's line is then the
  # silent deletion, and it is caught the same way: the gate is UNGATED.
  if ! runs_directly "$gate" && [ -z "$(runs_through "$gate")" ]; then
    echo "UNGATED: 'make ${gate}' exists but $WORKFLOW never runs it, directly or through a listed gate's recipe"
    note "a gate CI does not run is not a gate — add the step, or remove it from GATES in $0"
    note "comments and echoed text do not count; it must be a run: command, or a \$(MAKE) line of a gate CI runs"
    fail=1
    continue
  fi

  # Where it runs: its own step's job, or the job of the listed gate whose
  # recipe runs it.
  if runs_directly "$gate"; then
    check_blocking "$gate" "(^|[;&|(][[:space:]]*)make ${gate}([[:space:]]|\$)"
  else
    check_blocking "$gate" "(^|[;&|(][[:space:]]*)make $(runs_through "$gate")([[:space:]]|\$)"
  fi
done

# The commands of the Makefile's `lint` recipe: its tab-indented lines, up to
# the next rule, comment-only lines dropped — the recipe's long justification
# comments name the guards too, and must not satisfy enforcement.
lint_recipe="$(
  awk '
    /^lint:/ { in_lint = 1; next }
    in_lint && /^[^\t#[:space:]]/ { exit }
    in_lint && /^\t/ {
      body = $0
      sub(/^\t+/, "", body)
      if (body !~ /^#/) { print body }
    }
  ' "$MAKEFILE"
)"

if [ -z "${lint_recipe//[[:space:]]/}" ]; then
  echo "ci-gates-check: $MAKEFILE has no lint recipe — has the layout changed?"
  exit 1
fi

for guard in "${GUARDS[@]}"; do
  if [ ! -f "$guard" ]; then
    echo "MISSING GUARD: '${guard}' is listed as a guard but no such file exists"
    note "either restore the script or remove it from GUARDS in $0"
    fail=1
    continue
  fi
  # The same command-position rule as for the make targets: an echo naming the
  # guard, or a comment above a deleted step, is not a step.
  if ! grep -qE "(^|[;&|(][[:space:]]*)bash ${guard}([[:space:]]|$)" <<<"$ci_run_commands"; then
    echo "UNGATED: '${guard}' is a guard but $WORKFLOW never runs it"
    note "add the step to the bazel job, or remove it from GUARDS in $0"
    fail=1
  else
    check_blocking "$guard" "(^|[;&|(][[:space:]]*)bash ${guard}([[:space:]]|\$)"
  fi
  if ! grep -qE "(^|[;&|(][[:space:]]*)bash ${guard}([[:space:]]|$)" <<<"$lint_recipe"; then
    echo "NOT LINTED: '${guard}' is a guard but $MAKEFILE's lint recipe never runs it"
    note "add 'bash ${guard}' to lint, or remove it from GUARDS in $0"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "ci-gates-check: at least one quality gate is not enforced."
  exit 1
fi

printf 'All %d named gates and %d guards are enforced by %s, and all but the %d advisory block the required %s check.\n' "${#GATES[@]}" "${#GUARDS[@]}" "$WORKFLOW" "${#ADVISORY[@]}" "$REQUIRED_JOB"
