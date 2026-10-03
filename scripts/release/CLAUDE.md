<!-- updated: 2026-10-03T13:06:04Z -->
# scripts/release/

## Purpose

The release pipeline: WHETHER a merge to `main` releases anything
(`compute-bumps.sh`), HOW BIG the release is and the tags that publish it
(`cut-tags.sh`), and the same size question asked of a pull request before it
merges (`check-pr-size.sh`). The rules they share live once, in `lib/`. ADR
0007 (lockstep tags), ADR 0009 (a `go get`-able chain), ADR 0089 (one notion
of a consumer-visible change), ADR 0135 (the size is a maintainer's label).
The tree's general rules are in `scripts/CLAUDE.md`.

## Contents

| File | What it does | Run by |
|---|---|---|
| `compute-bumps.sh` | prints the tokens `pkg`, `framework` and `third-party`, each only when due, from the paths changed since the last release tag: `pkg/v*/**` or `pkg/go.mod`; `framework/**`; `third-party/**`; `internal/**` only when its Bazel reverse dependencies reach `//pkg/...`. Maintainer-only files are filtered out first (`lib/release-scope.sh`). `--explain` writes the verdict and its reason to stderr; `--require-bazel` refuses to decide rule 2 without Bazel | `sdk-release.yml`; `make release-dry-run` with `--dry-run` |
| `cut-tags.sh` | reads those tokens on stdin, sizes the release from the `release:*` labels of the merged pull requests in the range (or `--bump`), rewrites every chain module's `go.mod` without `replace` and pinned to the release version on a detached release commit, and tags every module of the chain at one version; re-reads the latest tag before the push and aborts on drift | `sdk-release.yml`; `make release-dry-run` with `--dry-run` |
| `check-pr-size.sh` | `--pr=<number>`: the size the squash of that pull request would publish, over every commit of the branch; exit 1 with the reason and the fix when no label decides it, 2 when GitHub cannot be read, 64 on bad usage | `release-size.yml`, on every push and label change of a pull request |
| `lib/` | the shared rules — see `lib/CLAUDE.md` | sourced by the three scripts above |
| `test-compute-bumps.bats`, `test-cut-tags.bats`, `test-check-pr-size.bats` | the regression suites: each builds a throwaway repository and asserts the tag the scripts compute; GitHub is a stub (`test-helpers.bash`) that runs the caller's own `jq` filter over fixtures and logs every call | `release-scripts-test.sh` |
| `test-helpers.bash` | `install_gh_stub` and the fixtures' helpers, loaded with `load test-helpers`; `.bash` so the runner's `*.bats` glob never runs it alone | — |
| `release-scripts-test.sh` | runs every `*.bats` here by glob, never a list; refuses to run zero suites; exits 127 naming the install routes when `bats` is not on `PATH` (it is never fetched) | `make release-scripts-check`, in `bazel-ci.yml`'s `shell-gates` job |
| `test-sync-versions.mjs.test.js` | a Node test of the docs site's `docs/site/scripts/lib/tag-format.mjs` (the tag regex, parsing, grouping, `versions.json`) | no lane — `docs/site`'s `npm test` globs `scripts/lib/*.test.mjs` only |

## Rules

- **The size is a label, never text.** A `release:*` label a maintainer set on
  the merged pull request decides; a `Release-bump:` line in the message is only
  read so that one asking for more than a patch with no label to decide it is
  refused loudly, before anything is published (#217, #224).
- **One chain, one version.** Any token releases every module `go.work` names
  but the root — `internal/{kernel,core,service}`, `pkg`, the framework and its
  connectors, the vendor modules — in that order (`lib/tag-format.sh`
  `chain_modules`), each requiring the one before.
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
- go.sum and a clean-room `go get` of the chain are settled at the first real
  release (ADR 0009): the tags must exist before their checksums can.

## Verify

```sh
make release-scripts-check    # needs bats and jq on PATH, bash >= 4
make release-dry-run          # the bump and the tags, computed, nothing pushed
```
