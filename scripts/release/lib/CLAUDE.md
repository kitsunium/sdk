<!-- updated: 2026-10-04T00:32:31Z -->
# scripts/release/lib/

## Purpose

The rules the release scripts share, each written once and sourced, because
every one of them was once two copies that disagreed: the tag format, the
notion of a path that can carry a consumer-visible change, and the size of a
release. Libraries, not entry points — nothing here is run on its own. See
`scripts/release/CLAUDE.md` for the scripts that source them.

## Contents

| File | Holds | Sourced by |
|---|---|---|
| `tag-format.sh` | the tag regexes and their checks — `SDK_TAG_REGEX` (`vX.Y.Z`, the SDK module at the root, major held to 0 or 1 — ADR 0162; `is_valid_tag`), `PKG_TAG_REGEX` (the `pkg/vX.Y.Z` history, read for the first root tag), `THIRD_PARTY_TAG_REGEX` and `CONNECTOR_TAG_REGEX` (`is_valid_vendor_tag`), `FAMILY_TAG_REGEX` (`pkg/v1/<family>/vX.Y.Z`, a family named like a major refused) and `INTERNAL_TAG_REGEX` (`internal/vX.Y.Z`, never a layer's) behind `is_valid_module_tag` (ADR 0171), `is_valid_release_tag` (all four); `module_tag_dir` (the module a release tag belongs to, `.` for the root; a tombstone names none), `module_tag_prefix`, `is_module_dir`; `remote_tags_present` (the given tags that exist on a remote, each compared with the FULL ref `git ls-remote --tags --refs` lists, the peeled `^{}` lines dropped, exit 2 when the remote cannot be read); `TOMBSTONE_TAG_REGEX` (`pkg/vX.Y.Z` and `framework/vX.Y.Z`, the tombstones the first root tag cuts — `is_valid_tombstone_tag`) and `is_pkg_tombstone` (a tag whose `pkg/` holds a go.mod alone); `vendor_modules`, the modules a release may tag beside the SDK, read from `go.work` (every `use` but `.`; refuses a `go.work` without `.`, and one that uses a directory that is no vendor module — `./pkg`, `./framework`, an `internal/` one); the version arithmetic `version_from_tag` (any shape), `strip_prerelease`, `next_patch`, `next_minor` (from an SDK or a pkg base, to an SDK tag), `version_sort` (whatever the prefix); `latest_sdk_tag`, `latest_pkg_tag` (a tombstone is no pkg release), `latest_release_tag` (the higher of the two; the root tag at one version), `latest_module_tag` (one module's highest stable tag, its shape a release tag's and its module exactly the directory asked — `.` is `latest_release_tag`) and `latest_vendor_tag` (the same, by its older name), `release_base` and `tag_base` (a tag's first parent) | `compute-bumps.sh`, `cut-tags.sh` |
| `release-scope.sh` | `release_scope_filter` (drops maintainer-only paths from a list) and `release_scope_any` (does any path under `pkg/`, `internal/`, `framework/` or `third-party/`, or the root `go.mod`, `LICENSE` or `README.md`, count? — ADR 0162) over one awk prelude: every `*.md` but `README.md`, case-insensitively, every `BUILD.bazel`, and since ADR 0171 every `*_test.go` and `testdata/**` path — no consumer build compiles one, and counted they re-tagged all thirteen vendor modules at v0.19.0 (#295, #220) — is maintainer-only | `compute-bumps.sh`, `cut-tags.sh`, `check-pr-size.sh` |
| `release-modules.sh` | the modules a release may tag and what each changed (ADR 0171): `release_modules` (go.work's `use` directories, `.` first; refuses a missing go.work, one without `.`, and a directory no tag shape names — `./pkg`, `./framework`, `./internal/core`), `module_path`, `module_pathspec` (a module's directory with every module nested in it excluded; for the root, every other module), `module_base` (its last tag's first parent), `module_range` (that base..HEAD, a caller's `--range`, or the empty tree for a module never tagged — `is_new_range`), `module_paths` (the paths it changed over a range, exit 2 when git fails) and `commit_touches_module` (did one first-parent commit change a path of it that counts — a diff against the first parent, never a pathspec'd `git log -1`, which answers an older commit) | `compute-bumps.sh`, `cut-tags.sh` |
| `release-size.sh` | `decide_size` over `label_size` (the `release:*` labels of a merged pull request, through `pr_labels_of_commit` and `release_gh`/`release_repo`) and `text_ask` (the largest `Release-bump:` a message asks for, anywhere in it), ranked by `size_rank` | `cut-tags.sh`, `check-pr-size.sh` |

## Rules

- **`tag-format.sh` and `docs/site/scripts/lib/tag-format.mjs` are one
  contract** (ADR 0007 §1): the docs site's version dropdown parses the tags
  the release cuts, so the two regexes change in the same commit — and so does
  what a tombstone is: the `pkg/vX.Y.Z` beside the root tag of its version is
  no release, here (`latest_pkg_tag`, `latest_release_tag`) and there
  (`dropTombstones`).
- **A category, not a list of names.** `release-scope.sh` answers "would a
  consumer see this file?" — pkg.go.dev renders a package's `README.md` and no
  other Markdown, and `go build` never reads `BUILD.bazel` — so a new kind of
  maintainer file needs no new entry. A `BENCH.md` cut `pkg/v0.4.4` from a
  wholly documentary diff when the rule was a list (#238).
- **The size is decided once.** `release-size.sh` is the one place a label and
  a message are weighed: a label decides; a message asking for more than a patch
  with no label is a refusal, never a silent patch (ADR 0135).
- **Everything reads to EOF.** No `grep -q`, no `grep -c`, no pipeline into an
  early-exiting reader — the SIGPIPE shape has cost a release twice (ADR 0085).
- **A missing or unreadable input is an error.** `vendor_modules` refuses to
  guess the release's modules from a `go.work` it cannot read.
- **Every shape is anchored, and every selection goes through one.** A glob
  (`git tag -l 'pkg/v*'`) only narrows; the anchored regex decides — a prefix
  match read `pkg/v1/data/v0.1.0` as pkg's history or as a tombstone (ADR 0171).
  A remote's tags are compared as full refs: `ls-remote` matches a pattern
  against a ref's tail.
- **A hyphen is a pre-release only in the version.** `stable_tags` looks for it
  after the last `/`: `third-party/x-crypto/v0.18.0` is stable.

## Verify

```sh
make release-scripts-check    # the BATS suites of scripts/release/ exercise every function here
```
