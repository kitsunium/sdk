<!-- updated: 2026-10-04T00:30:00Z -->
# scripts/release/

## Purpose

The release pipeline: WHETHER a merge to `main` releases anything
(`compute-bumps.sh`), HOW BIG the release is and the tags that publish it
(`cut-tags.sh`), and the same size question asked of a pull request before it
merges (`check-pr-size.sh`). The rules they share live once, in `lib/`. ADR
0162 (one tag on the SDK module per release, a vendor module tagged when it
changes), ADR 0007 (the release workflow), ADR 0009 (a `go get`-able release),
ADR 0089 (one notion of a consumer-visible change), ADR 0135 (the size is a
maintainer's label). The tree's general rules are in `scripts/CLAUDE.md`.

## Contents

| File | What it does | Run by |
|---|---|---|
| `compute-bumps.sh` | prints the one token `sdk` when a release is due, from the paths changed since the last release tag: `pkg/**`, `framework/**` outside a connector, or the root `go.mod`, `LICENSE` or `README.md` (rule 1 — the SDK module); `third-party/**` or `framework/connectors/**` (rule 1c — a vendor module); `internal/**` only when its Bazel reverse dependencies reach `//pkg/...`, `//framework/...` or `//third-party/...` (rule 2). Maintainer-only files are filtered out first (`lib/release-scope.sh`). `--explain` writes the verdict and its reason to stderr; `--require-bazel` refuses to decide rule 2 without Bazel | `sdk-release.yml`; `make release-dry-run` with `--dry-run` |
| `cut-tags.sh` | reads the token on stdin (`sdk`; `pkg`, `framework`, `third-party` and `vN` still mean it), sizes the release from the `release:*` labels of the merged pull requests in the range (or `--bump`), and computes the version from the newest `vX.Y.Z` — or, before the first, the newest `pkg/vX.Y.Z`. Tags the SDK module `vX.Y.Z` and, at the same version, each vendor module `go.work` names that changed since the first parent of its own last tag (or has none), its `go.mod` rewritten to require the SDK at the release with no `replace`, on a detached release commit — empty when nothing is rewritten; re-reads the latest tag before the push and aborts on drift. stdout: the SDK's tag, then the vendor tags | `sdk-release.yml`; `make release-dry-run` with `--dry-run` |
| `check-pr-size.sh` | `--pr=<number>`: the size the squash of that pull request would publish, over every commit of the branch; exit 1 with the reason and the fix when no label decides it, 2 when GitHub cannot be read, 64 on bad usage | `release-size.yml`, on every push and label change of a pull request |
| `lib/` | the shared rules — see `lib/CLAUDE.md` | sourced by the three scripts above |
| `test-compute-bumps.bats`, `test-cut-tags.bats`, `test-check-pr-size.bats` | the regression suites: each builds a throwaway repository and asserts the tag the scripts compute — and the publish path pushes to a bare repository the test owns, so the release commit and the tags on it are asserted too; GitHub is a stub (`test-helpers.bash`) that runs the caller's own `jq` filter over fixtures and logs every call | `release-scripts-test.sh` |
| `test-helpers.bash` | `install_gh_stub` and the fixtures' helpers, loaded with `load test-helpers`; `.bash` so the runner's `*.bats` glob never runs it alone | — |
| `release-scripts-test.sh` | runs every `*.bats` here by glob, never a list; refuses to run zero suites; exits 127 naming the install routes when `bats` is not on `PATH` (it is never fetched) | `make release-scripts-check`, in `bazel-ci.yml`'s `shell-gates` job |
| `test-sync-versions.mjs.test.js` | a Node test of the docs site's `docs/site/scripts/lib/tag-format.mjs` (the tag regex, parsing, grouping, `versions.json`) | no lane — `docs/site`'s `npm test` globs `scripts/lib/*.test.mjs` only |

## Rules

- **The size is a label, never text.** A `release:*` label a maintainer set on
  the merged pull request decides; a `Release-bump:` line in the message is only
  read so that one asking for more than a patch with no label to decide it is
  refused loudly, before anything is published (#217, #224).
- **One module, one tag; a vendor module when it changed** (ADR 0162). Every
  release tags the SDK module `vX.Y.Z`; a module `go.work` names beside it
  (`lib/tag-format.sh` `vendor_modules`, which refuses `./pkg`, `./framework`
  or an `internal/` directory back in the workspace) is tagged `<dir>/vX.Y.Z`
  only when a file of it a consumer sees changed since the commit its own last
  tag was cut from, or when it has never been tagged. A module that keeps its
  tag keeps a go.mod requiring the SDK release it was cut with. Its own
  directory is all that is measured: an `internal/` fix the module uses cuts a
  release (rule 2 counts `//third-party/...`) but does not re-tag it, and its
  consumer takes the fix by requiring the newer SDK release.
- **The first root tag is held while its base is a `pkg/vX.Y.Z` tag.** Until a
  `vX.Y.Z` above the pkg history exists, the version continues the newest
  `pkg/vX.Y.Z` (v0.17.0 → v0.18.0 for a minor), and the cut is held — exit 3 —
  unless `--allow-bootstrap`, which a maintainer's dispatch of SDK Release
  passes (ADR 0009). A stray root tag below the history — the `v0.0.0` the
  proxy knows for the path — does not lift the hold: asking "is there no
  `vX.Y.Z` yet?" instead let one through, and an automatic run cut the first
  release (seen red in the suite first).
- **From a `pkg/` base a patch is refused** (exit 65, like an undecided size):
  the first root tag moves every consumer to another module, which v0 says with
  a minor at least, and the proxy keeps a v0.17.1 for good. The refusal names
  `release:minor` and `bump=minor`, and comes before the hold, so an automatic
  run says it as soon as the merge lands.
- **A release commit always exists.** It is the tags' commit, a detached child
  of the main commit the release was cut from — `--allow-empty` when nothing is
  rewritten —, because the next range and every vendor module's next
  measurement start at a tag's first parent.
- **The publication subshell honours errexit.** It runs under its own
  `set -e` and its status is read after it: under `( … ) || rc=$?` bash ignored
  errexit inside, and a refused go.mod was released anyway.
- **The range walk is `--first-parent`**, so a commit a merge brought in never
  sizes a release, and a merge's own paths are read with `-m --first-parent`.
- **No `grep -q`, no `grep -c`, no pipeline into an early-exiting reader**: the
  SIGPIPE shape has cost this repository a release twice (ADR 0085).
- **A defect here produces a plausible version, not an error**, so each fix
  lands with a named BATS case seen red against the pre-fix script.

## Attention points

- `compute-bumps.sh` and `cut-tags.sh` use `mapfile`, so they need bash ≥ 4:
  CI runs them under Ubuntu's bash 5, and macOS's `/bin/bash` 3.2 cannot run
  them (the `--require-bazel` cases of the suite fail there for that reason).
  The pre-commit guards' bash 3.2 rule does not extend to this directory.
- go.sum and a clean-room `go get` of a vendor module are settled at its first
  real release (ADR 0009): the tags must exist before their checksums can.

## Verify

```sh
make release-scripts-check    # needs bats and jq on PATH, bash >= 4
make release-dry-run          # the bump and the tags, computed, nothing pushed
```
