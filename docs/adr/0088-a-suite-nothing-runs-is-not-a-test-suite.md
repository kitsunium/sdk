# ADR 0088 — a suite nothing runs is not a test suite, and a gate is a name CI says out loud

- **Status**: Accepted
- **Date**: 2026-09-14
- **Deciders**: SDK maintainers
- **Related**: [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) (whose §Deferred this closes), [ADR 0007](0007-sdk-release-and-versioning.md) (bump semantics and the unsmugglable trailer), [ADR 0009](0009-pkg-public-module-resolvability.md) (the detached release commit, the `major` refusal), [ADR 0004](0004-sdk-bazel-build-system.md) (`bazel-ci.yml` is the gating lane)

## Context

ADR 0085 fixed three defects in the two scripts that decide which tag every
merge to `main` gets, and shipped 32 BATS tests that reproduce them. Its
§Deferred then recorded, in as many words:

> **Nothing runs the BATS suite.** `scripts/release/*.bats` is executed by no CI
> lane and no `make` target; the regression tests added here were run locally
> with `bats-core` v1.11.1. Wiring a lane touches `bazel-ci.yml` and is left to
> a change that owns that file.

Re-verified on `origin/main` at `0218b98` before this change: `grep -rn bats
Makefile .github/` returns nothing. Zero occurrences, in the Makefile and in all
ten workflows. The suite had never been executed by anything but a human.

That is a worse position than having no tests, because the three defects it
covers do not fail loudly. Each of them produces a **plausible version number**
instead of an error, so the only place the damage is visible is the tag itself,
after it has been pushed and published.

### Defect 1 — `separator=` is not an output separator

`git` parses the `%(trailers:…)` OPTION LIST on commas. Writing `separator=,`
makes the comma the list delimiter and leaves the separator **empty**, so
repeated trailers are concatenated into one field. Measured here on git 2.47.3,
on a commit carrying `Release-bump: mi` and `Release-bump: nor`, piped through
`cat -A` so the delimiter is visible:

```
######## separator=, ########
50a111565bd03faca756153aba8118ef7ecc3baf minor$
######## separator=%x1F ########
50a111565bd03faca756153aba8118ef7ecc3baf mi^_nor$
```

Two halves of a word read back as the single value `minor`, and a minor release
is cut. Nothing in the commit, the diff, or the run log says so.

The same construct is broader than that one case: with `separator=,`, a commit
carrying `Release-bump: minor` **twice** yields `minorminor`, which ranks as
unrecognised and silently degrades a minor to a patch. Both shapes are covered.

### Defect 2 — a walk without `--first-parent` reads the contributor's trailer

[ADR 0007](0007-sdk-release-and-versioning.md) §2 gates a minor on a trailer "an attacker cannot smuggle through a PR
body" by reading it from the merge commit — the one message a maintainer writes.
A range walk without `--first-parent` descends into the commits the merge
brought IN, which are the contributor's own. Five commits on this repository
carry `Release-bump: minor`, touch `pkg/`, and are not on `main`'s first-parent
line.

### Defect 3 — a true merge shows no files under a plain `--name-only`

The per-commit path scoping is what keeps a trailer written in an unrelated docs
merge from sizing a `pkg` release. But a merge commit reports NO files under a
plain `git log --name-only`, so a maintainer's trailer on one was scoped against
an empty list and counted for nothing — 14 of this repository's 33
trailer-bearing commits are merges.

### Defect 4 — the suite was not hermetic

Found while wiring the lane, not predicted by it. The disposable fixture repos
inherit the host's `core.hooksPath`, so every `git commit` and `git merge` in
the suite ran the developer's own hooks. With a host `pre-commit` that refuses,
the suite fails in `setup`, on code that is correct:

```
not ok 1 the largest trailer in the range wins
# (from function `setup' in test file test-cut-tags.bats, line 73)
#   `git commit -q -m "init"' failed
# hôte: politique locale refusée
```

Neither a repo-local `core.hooksPath` nor `GIT_CONFIG_GLOBAL=/dev/null` fixes
this, because a hooks path injected at `-c` precedence outranks both — measured,
all three shapes tried. Only a command-line argument wins.

`--no-verify` alone is not that argument, which review caught and measurement
confirmed: it skips the VERIFICATION hooks — `pre-commit` and `commit-msg` — and
leaves `post-commit`, `post-merge` and `post-checkout` running inside the
disposable repository, free to mutate state the later assertions read. With a
host `post-commit` that appends to the fixture's own source file:

```
git commit --no-verify                            -> POST-COMMIT HOTE A TOURNE
git -c core.hooksPath=<empty> commit --no-verify   -> propre
```

So every fixture git command goes through one helper that carries
`-c core.hooksPath` to an empty directory, and keeps `--no-verify` behind it.
Verified with hostile `post-commit` AND `post-merge` hooks installed: 34 of 34
pass and no host hook runs.

### Defects 5, 6 and 7 — the same pipeline, three more times, one of them inverted

Found by sweeping the repository for the construct after defect 4, rather than
by reading the diff. `printf … | grep -q` under `pipefail`: `grep -q` exits on
its first match, the writer takes SIGPIPE, and the pipeline reports 141 — false
— for input that DID match. It needs an input past the 64 KiB pipe buffer.

**`.githooks/commit-msg`** is the worst of the three, because the pipeline is the
condition of an `if`, so the failure is not "the gate breaks" but "the gate
allows what it exists to refuse". `set -e` does not rescue it: a failing command
in an `if` condition is exempt by design. Measured on the hook as it stood,
`Co-Authored-By: Claude` placed near the top with filler after it, 40 runs per
cell — "pass" means the commit went through:

| body | pattern early | pattern late |
|---|---|---|
| 1 000 | 0/40 | 0/40 |
| 64 000 | 0/40 | 0/40 |
| 70 000 | 1/40 | 0/40 |
| 96 000 | 27/40 | 0/40 |
| 400 000 | 40/40 | 0/40 |

The right-hand column is the control: with the pattern at the END, grep must
read everything, there is no early exit and no signal, and the gate holds at
every size. A gate whose correctness depends on where in the input the match
happens to sit is not a gate. After the fix, 0/40 at 400 KB.

The exit status, captured directly:

```
exit du pipeline quand le motif MATCHE = 141
--- la condition if est-elle donc fausse ? ---
if FAUX -> LAISSE PASSER
```

**`compute-bumps.sh`** asked `bazel query "rdeps(//pkg/…, //<mod>/…)" | grep -q .`
inside an `if`. `grep -q .` matches line 1 and exits immediately, so this is the
worst case rather than a corner of it. With a stub emitting 608 KB:

```
octets produits par le faux bazel : 608894
if FAUX  -> need_bump=0 (AUCUNE RELEASE)
exit du pipeline seul : rc=141
```

A change to `internal/` whose rdeps DO reach `//pkg/...` cut no release at all.

**`cut-tags.sh`** scoped each commit with
`git log -1 --name-only … | grep -qE '^pkg/' || continue`. On a commit touching
`pkg/` plus enough other paths, the pipeline is 141, `|| continue` skips the
commit, and its `Release-bump` trailer is never counted — the release falls back
to a patch. Which is ADR 0085's own defect, reopened by a different route.
Measured on this shape: the first `pkg/` line is at position 1 (worst case), 400
paths (85 KB) fail the pipeline 9 times in 10, 1600 paths 10 in 10.

All three take the same remedy: no pipeline. A here-string for the hook, a
process substitution for the two release scripts — in both the writer's status
stays out of the pipeline, so `pipefail` has nothing to propagate.

The sweep also found the construct in `.devcontainer/images/.claude/scripts/`,
including `permission-request.sh`, which is the Claude Code-side guard this hook
was written to mirror and carries the identical inverted shape. Those files are
template-inherited and `.github/CLAUDE.md` forbids editing them for SDK reasons,
so they are reported and left — see §Deferred.

## Decision

**1. `scripts/release/release-scripts-test.sh` runs the suites.** One entry
point, named after the ktn-linter target it mirrors, so the same command works
locally and in CI.

**2. `bats-core` is a prerequisite, not a vendored artefact.** The runner
resolves `bats` from `PATH` and, when it is absent, exits 127 naming the three
install routes. It does **not** fetch it. `make lint` already refuses to depend
on network egress (the `guard` target says so); a gate that clones a third-party
repository on every run is a flake, not a gate. CI installs the distro package
in the same job that runs the suite.

**3. `scripts/ci-gates-check.sh` holds the gate manifest.** A `make` target CI
never runs is documentation. The script fails when a listed gate has no target,
is not `.PHONY`, or is not invoked by `bazel-ci.yml` — matched against the
workflow's executable `run:` commands, never its raw text, because a gate named
only in a surviving comment would otherwise satisfy enforcement after its step
was deleted. Measured on exactly that edit: the raw-text form reports green.

Its own target is in the manifest, which catches every gate but itself. Nothing
that has been deleted can report its own deletion; that case belongs to branch
protection, and is currently unowned — see §Deferred.

**4. The lane is a sibling job, `shell-gates`.** Neither gate needs Bazel or Go,
so neither belongs behind the 120-minute Bazel job or inside a matrix that would
run it eleven times.

**5. `hooks-check` is a third gate**, with its own BATS suite over
`.githooks/`. The commit-msg hook is policy enforcement, not convenience, and it
had been wrong since it was written without anyone noticing, because it was only
ever exercised below the pipe buffer.

**6. Every fixture git command disables hooks at command-line precedence**, and
keeps `--no-verify` behind it. `git commit-tree` is plumbing and runs no hooks,
so it is left alone.

**7. The suite list is globbed, never enumerated.** The Makefile target and the
CI step both describe the runner as covering `scripts/release/*.bats`; a
hard-coded list makes that false the moment a third suite is added, which would
then be committed, reviewed and executed by nothing — this change's own defect,
reintroduced by its own fix.

## Consequences

- 42 tests run on every pull request: 34 over the release scripts (32 inherited
  from ADR 0085, plus one each for defects 6 and 7) and 8 over the commit-msg
  hook. All pass; one skips (the `bazel`-dependent rdeps path, skipped when
  bazel is present).
- Every one of them was observed RED against the code it guards before being
  accepted. The three hook tests failed 3 of 8 before the fix, with the trailing-
  attribution control passing throughout — which is what identified the
  mechanism rather than merely the symptom.
- Each of the three ADR 0085 defects was confirmed to make its own test go RED
  when reintroduced into `cut-tags.sh`, and GREEN when reverted. The suite had
  never been red, because it was written alongside the fix; this is the first
  evidence that it can be.
- `ci-gates-check` was likewise confirmed red in all three of its failure modes
  (no target, not `.PHONY`, not invoked by CI) before being wired green.
- `bazel-ci.yml` gains the first `make` invocations in its history. Everything
  else in it calls `bazel` or `bash` directly, which is precisely why the
  Makefile↔CI link needed asserting: it did not exist.

## Breaking changes

None. The change adds a CI job, two `make` targets and two scripts. No public
API, no module path, no tag shape and no release behaviour is altered — the
release scripts themselves are unchanged except for the test fixtures'
`--no-verify`, which affects no production path.

## Alternatives considered

### Rewrite the suites as a plain bash harness, as ktn-linter did

ktn-linter's `release-scripts-test.sh` is hand-rolled bash with `ok()`/`bad()`
counters and uses no BATS at all. Copying that shape here would mean rewriting
32 working, well-commented assertions into a form with weaker failure reporting,
and throwing away the per-test isolation that makes each fixture disposable. The
dependency is one distro package. The rewrite is the expensive half of the
comparison, not the cheap one.

### Put the gates in the `bazel` job

It already has `checkout` and `setup-go`. It also has `timeout-minutes: 120` and
a full Bazel toolchain, and a broken release script would be reported after the
build rather than in the first minute of the run.

### Make `ci-gates-check` audit the `bash scripts/…` steps too

Rejected as written, deferred as a question — see below. A renamed script
invoked as `bash scripts/x.sh` already fails loudly at exit 127, so the check
would add little where it is cheap, and would require judgement calls where it
would add most.

## Deferred

- **Four of nine project-local checks are enforced by nothing but an opt-in
  local hook.** `.githooks/pre-commit` runs every executable
  `scripts/pre-commit/*.sh` by glob, and `scripts/install-hooks.sh` must be run
  by hand after clone. Measured: `check-bench-md.sh`,
  `check-error-codes-drift.sh`, `check-ktn-phases-1-7.sh` and
  `check-pkg-docs.sh` are invoked by neither `make lint` nor `bazel-ci.yml`.
  Whether each belongs in CI is four separate decisions about cost and
  flakiness, not one.
- **`make lint` and `bazel-ci.yml` do not run the same set.** CI additionally
  runs `check-readme-drift.sh` and `check-readme-determinism.sh`, which need
  `gomarkdoc` installed. The asymmetry is defensible; it is not recorded
  anywhere, and nothing would notice it widening.
- **One of the 32 tests runs in no environment we have.** `internal-only change
  without bazel emits nothing (graceful)` skips when `bazel` is on `PATH` — and
  it is, both on `ubuntu-latest` and on the development box. Measured from the
  first CI run of this lane: `ok 6 … # skip bazel present, rdeps path active`.
  The plan says 32; 31 execute. Making it run means scrubbing `bazel` from
  `PATH` for that one test, which changes what the test asserts, so it is named
  here rather than changed quietly.
- **`shell gates` is not a required status check, so it does not block a
  merge.** Measured on `main`'s protection at the time of writing:
  `required_checks: ["bazel"]`. Both gates run and report on every PR, but a red
  one is advisory until somebody adds the job to the required set — a repository
  setting, not a file in this change. It is the single largest remaining gap
  between "the gate runs" and "the gate gates".
- **The suites `skip` rather than fail when `go` or `jq` is absent**, which in CI
  would be a green gate that tested nothing. The runner now asserts both when
  `CI` is set and refuses to start otherwise; the local skip is kept, because a
  developer without jq should still get the assertions that do not need it. The
  assertion is a backstop for a runner-image change: measured on the first run of
  this lane, `ubuntu-latest` carries both.
- **The same pipeline survives in `.devcontainer/images/`**, including
  `permission-request.sh`, the Claude Code-side guard `.githooks/commit-msg`
  mirrors — same `if <writer> | grep -qE "$pattern"`, same inverted failure.
  Those files are template-inherited and `.github/CLAUDE.md` forbids editing
  them for SDK reasons, so the finding belongs upstream in the devcontainer
  template rather than here.
- **`scripts/pre-commit/check-ktn-phases-1-7.sh` and
  `scripts/pre-commit/check-audit-coverage.sh` carry a pipe into `grep -q`
  too.** Neither was measured: both are outside the release/hooks perimeter this
  ADR owns, and the audit-coverage one is a `grep | grep -qv` whose direction
  has to be worked out before it can be called a defect. Named so the sweep is
  on record as incomplete rather than reported as clean.
- **The distro `bats` floats.** CI takes whatever `ubuntu-latest` ships. Pinning
  it would mean vendoring, which decision 2 rejects for this gate; the runner
  prints the version it used so a version-dependent failure is at least
  diagnosable from the log.
- **`Release-bump: patch` is still not vocabulary** — carried forward unchanged
  from ADR 0085 §Deferred. It ranks with "unrecognised" and is harmless, but an
  unrecognised value is still accepted silently rather than refused loudly.

## References

- [ADR 0085 §Deferred](0085-both-halves-of-a-release-read-the-same-range.md) — the debt this closes
- [`git log --format` `%(trailers:…)`](https://git-scm.com/docs/git-log#Documentation/git-log.txt-emtrailersoptionsem) — `key`, `valueonly`, `separator`
- [`git commit --no-verify`](https://git-scm.com/docs/git-commit#Documentation/git-commit.txt---no-verify) — the only neutralisation that outranks an injected `core.hooksPath`
- [bats-core](https://github.com/bats-core/bats-core) — the suite's runner
