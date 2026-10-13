<!-- updated: 2026-10-05T14:00:00Z -->
# scripts/

## Purpose

Repository tooling, not library code: the pre-commit guards, the release
scripts, the CI helpers, and the checks that keep each of them a real gate.
Exempt from rule 8's `README.md` requirement (root `CLAUDE.md`), because nothing
here is a Go package.

## Contents

| Path | What it is |
|---|---|
| `pre-commit/` | the guards `make lint` and `bazel-ci.yml` run (ADR 0153), each an executable `check-*.sh` that finds the repository root from its own path (eight also take the workspace path as their first argument); `test-pre-commit-guards.bats` pins them, and keeps them runnable on a Mac as shipped |
| `release/` | `compute-bumps.sh` (WHICH modules release, each since its own tag — ADR 0171), `cut-tags.sh` (HOW BIG each is, from the `release:*` labels of the merges that touched it — ADR 0135 —, and the tags and manifest), `release-notes.sh`, `check-pr-size.sh`, their BATS suites (`test-*.bats` over `test-helpers.bash`, run by `release-scripts-test.sh`) and `lib/` (`tag-format.sh`, `release-scope.sh`, `release-size.sh`, `release-modules.sh`); also `test-sync-versions.mjs.test.js`, a Node test of `docs/site/scripts/lib/tag-format.mjs` that no lane runs — `docs/site`'s `npm test` globs `scripts/lib/*.test.mjs` only |
| `ci/` | `go-modules.sh`, the module census every module-looping lane reads (ADR 0137); `platforms.sh`, the twelve GOOS/GOARCH cells as one table; and `vuln-check.sh`, the govulncheck gate over the census (ADR 0136) |
| `check-layer-deps.sh` | the layer firewall on the build graph: seven `bazel query` expressions that must be empty (ADR 0068; the last three are the framework's, ADR 0147) |
| `ci-gates-check.sh` | a listed gate must have a `make` target, be `.PHONY`, and be invoked by `bazel-ci.yml` — by a step of its own, or by the recipe of a listed gate CI runs (`lint-check` runs `api-check`); a listed guard (`GUARDS`: the `bash` checks `make lint` runs) must exist, be a `run:` step of the `bazel` job and a line of the `lint` recipe (ADR 0088); and every gate and guard must run in a job whose failure fails the required `bazel` check — `bazel` itself, or a job it `needs:`, whose result it checks and which cannot skip it (`!cancelled()`), else `NOT BLOCKING` unless listed in `ADVISORY` with its reason (#236); its BATS cases are in `ci/test-ci-scripts.bats` |
| `ci-scripts-test.sh`, `pre-commit-test.sh` | run the BATS suites of `ci/` and `pre-commit/` |
| `cross-platform-audit.sh` | the local twin of `bazel-ci.yml`'s `cross-build`: every module of the census built and vetted (tests included) for every cell of `ci/platforms.sh`, printed as a matrix; needs bash 4 |
| `regen.sh` | `make regen`: refuses unless `kit` is on PATH at `design/sdk.yaml`'s `project.kit.version` the tree has no tracked change and no untracked file carries the header; deletes exactly the tracked files whose first line is kit's generated header (`tools/genindex/digests.go`'s `kitHeader`) and clears the `# BEGIN kit gen` … `# END kit gen` section of `tools/alloc-lane-targets.txt`, runs `kit gen` then `make api`, and fails on any `git diff` or any file it adds that git does not track. Local only — it needs kit, which CI does not have; bash 3.2 and BSD tools like the guards |
| `from-zero.sh` | `make from-zero` (ADR 0169): in a temporary copy of every file git tracks or would track — never the working tree —, deletes every file whose first line is kit's header and kit's lane section as `regen.sh` does, runs `kit gen -stubs` and requires every module of the copy — every `go.mod` it holds, untracked ones included, testdata left out as the census leaves it — to `go build` and `go vet`, counting the stubs per package, then `kit gen` and `make api`, and fails on any file added (git's ignore rules applied), missing or changed against the working tree; `FROM_ZERO_KEEP=1` keeps the copy. Local only; bash 3.2 and BSD tools |
| `kit-coverage.sh` | `make kit-coverage` (ADR 0169): `kit design coverage`'s table, then the ten packages with the most exported declarations written by hand; a report, exit 0 |
| `kit-pinned.sh` | sourced, never run: what `regen.sh`, `from-zero.sh` and `kit-coverage.sh` share so the three decide alike — `kit_pinned NAME` refuses unless `kit` on PATH is `design/sdk.yaml`'s `project.kit.version` (setting `KIT_PINNED`), `KIT_HEADER` the first line of every file kit generates, `kit_clear_section FILE` kit's `# BEGIN kit gen` section of the allocation lane removed (ADR 0169) |
| `gen-error-codes.sh` | writes `docs/error-codes.yaml` from `docs/api` (`make error-codes`, which `make api` runs): `tools/genindex -write-error-codes`, every errs.Code constant a package declares |

## The cells are one table

Twelve: linux on amd64, arm64, 386 and arm; darwin/arm64; windows/amd64; the
four BSDs on amd64; illumos/amd64 and solaris/amd64 (ADR 0144 — two GOOS values,
since `runtime.GOOS` tells them apart although the `solaris` build tag selects
both). `ci/platforms.sh` prints them, one `goos/goarch` per line from its one
here-document: `cross-platform-audit.sh` loops over it, and tools/genindex reads
the here-document without running the script, to judge doc links and to write
`docs/api` on every cell. `bazel-ci.yml`'s `cross-build` matrix has to be
written in the workflow, so `pre-commit/check-platforms.sh` — in `make lint`
and the `bazel` job — fails until the two name the same cells in the same
order. The local audit used to carry a third copy, which nothing checked.

## The guards run on the tools a Mac ships

Every `pre-commit/*.sh` runs under macOS's `/bin/bash` 3.2 and
the BSD `find`, `sed`, `sort` and `wc` in `/usr/bin`, as well as under bash 5
and GNU tools, with nothing installed (#260). So: no `readarray` or `mapfile`,
no associative array, no `${x,,}`, no `find -printf`; an array that may be
empty is not expanded under `set -u`, which bash 3.2 reports as unbound; and a
`wc -l` count loses the spaces BSD pads it with before it is printed. CI only
has bash 5 and GNU find, so `test-pre-commit-guards.bats` reads the sources for
the constructs that broke the guards before, rather than trusting a run there.
`gen-error-codes.sh` is held to the same rule. One guard needs more than a Mac
ships: `check-error-codes-drift.sh` runs tools/genindex, so it needs the Go
toolchain every `make lint` needs anyway.

## Rules from ADR 0088

Superseded by ADR 0154 (the charter); ADR 0088 stays as the incident's record, and its rules live here.

- **A suite counts only when a gate CI names runs it.**
  `release/release-scripts-test.sh` runs every `release/*.bats` by glob, never a
  list; a new runner needs a `make` target, `.PHONY`, invoked by `bazel-ci.yml`.
  `ci-gates-check.sh` holds that manifest, matches the workflow's executable
  `run:` commands rather than its text, and lists itself; a guard `make lint`
  runs is listed in its `GUARDS` the same way, in the commit that wires it.
- **`bats` is a prerequisite on `PATH`, never fetched** (exit 127 naming the
  install routes); gates needing neither Bazel nor Go run in `shell-gates`,
  which blocks a merge only because the required `bazel` job needs it and
  fails on its result — `ci-gates-check.sh` holds that link (#236).
- **A fixture's git commands disable hooks at command-line precedence**, so a
  host's `core.hooksPath` never reaches a test; every test is seen red against
  the defect it guards before it is accepted.
- *Lesson*: the 32 BATS tests guarding release sizing ran in no lane, and each
  defect they cover produced a plausible version rather than an error. (Its
  hook gate went with ADR 0153.)

## ktn-linter in CI (#236)

`make lint` runs `ktn-linter`, and `make lint-ktn-check` is a gate: listed in
`ci-gates-check.sh`'s `GATES`, run by the required `bazel` job, blocking.

- **The binary is the vendor's release, from its public channel.**
  `kodflow/ktn/setup` (pinned by commit in `bazel-ci.yml`) installs it — its
  installer verifies the published checksum — and runs `ktn-linter license
  status`, which proves the licence with the runner's OIDC token: no key, no
  repository secret, no device seat. The `bazel` job holds `id-token: write`
  for that alone. The run is covered when the vendor's signed roster lists the
  numeric id of the repository's owner (`repository_owner_id`), which for
  `kitsunium/sdk` is the `kitsunium` organisation — not the maintainer's own
  account. The binary is rolling-latest by the vendor's design (one release is
  downloadable, and the roster's minimum version upgrades an older one), so
  the version a run used is read from its log.
- **The one skip: a pull request from a fork, or from Dependabot.** GitHub
  gives those runs no id-token, so the licence cannot be proven there; the job
  detects exactly those two, warns (`::warning title=ktn-linter gate did not
  run::`) and skips the install and the lint. That is acceptable because a
  fork pull request cannot merge without a maintainer, whose own pushes lint:
  pushing the fork's branch to this repository lints it before the merge, and
  the merge commit on `main` is linted after it either way. A Dependabot pull
  request touches only workflow pins (`.github/dependabot.yml`), and its merge
  to `main` is linted too. Any other
  run that finds no token FAILS rather than skips: there the token is missing
  because the permission was lost, and a gate that quietly skips itself is
  the shape #236 exists to close. No step continues on error.
- **`make lint-ktn-install` is the local alternative**, for a maintainer with
  read access to the private source: it builds the commit the Makefile pins,
  whose licence gate is inert by construction (the maintainers' mode upstream
  documents), and refuses another commit, a dirty checkout, or a binary
  reporting another version. No secret is involved — the clone uses the
  maintainer's own account. CI does not run it. Move its pin when CI reports a
  newer release, or the local and CI verdicts can differ.
- **Before this**, CI built the linter from that private source, fetched with
  a repository secret that was never created, so all three steps were skipped
  on every run and the target was kept out of `GATES`.

## Rules from ADR 0137

Superseded by ADR 0154 (the charter); ADR 0137 stays as the incident's record, and its rules live here.

- **The modules are a census, not a list**: `ci/go-modules.sh` prints every
  module whose `go.mod` git tracks (`testdata` excluded; an empty or unreadable
  census is an error), and every lane that loops over modules reads it.
- **A lane that must skip a module names it where it loops**, with the lane
  that covers it instead (rule 12); none does today — the root is the SDK
  module since ADR 0162, no longer the empty anchor every loop skipped.
- **The census and its use are tested**: `ci/test-ci-scripts.bats`
  (`make ci-scripts-check`) pins it and asserts `cross-build` and `test-386`
  read it, and that no census loop skips the root.
- *Lesson*: four hand-written module lists had drifted apart, and
  `tools/genindex` and `tools/sdkguard` — 477 tests — never ran on 32 bits.

## Do NOT

- Enumerate modules in a script that loops over them — read
  `ci/go-modules.sh` (ADR 0137).
- Add a suite nothing runs: a new `*.bats` beside the others is picked up by its
  runner's glob, and a new runner needs a `make` target listed in
  `ci-gates-check.sh` (ADR 0088).

## Subtree

- `ci/` — see `ci/CLAUDE.md` (the census and the vulnerability gate)
- `pre-commit/` — see `pre-commit/CLAUDE.md` (each guard, what fails it and what runs it)
- `release/` — see `release/CLAUDE.md` (whether, how big, the tags) and `release/lib/CLAUDE.md` (the rules the release scripts share)
