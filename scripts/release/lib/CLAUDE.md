<!-- updated: 2026-10-03T13:06:04Z -->
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
| `tag-format.sh` | the tag regexes and their checks — `SDK_TAG_REGEX` (`vX.Y.Z`, the SDK module at the root, major held to 0 or 1 — ADR 0162; `is_valid_tag`), `PKG_TAG_REGEX` (the `pkg/vX.Y.Z` history, read for the first root tag), `THIRD_PARTY_TAG_REGEX` and `CONNECTOR_TAG_REGEX` (`is_valid_vendor_tag`), `is_valid_release_tag`; `vendor_modules`, the modules a release may tag beside the SDK, read from `go.work` (every `use` but `.`; refuses a `go.work` without `.`, and one that uses a directory that is no vendor module — `./pkg`, `./framework`, an `internal/` one); the version arithmetic `version_from_tag` (any shape), `strip_prerelease`, `next_patch`, `next_minor` (from an SDK or a pkg base, to an SDK tag), `version_sort` (whatever the prefix); `latest_sdk_tag`, `latest_pkg_tag`, `latest_release_tag` (the higher of the two), `latest_vendor_tag`, `release_base` and `tag_base` (a tag's first parent) | `compute-bumps.sh`, `cut-tags.sh` |
| `release-scope.sh` | `release_scope_filter` (drops maintainer-only paths from a list) and `release_scope_any` (does any path under `pkg/`, `internal/`, `framework/` or `third-party/`, or the root `go.mod`, `LICENSE` or `README.md`, count? — ADR 0162) over one awk prelude: every `*.md` but `README.md`, case-insensitively, and every `BUILD.bazel` is maintainer-only; nothing else is excluded, `*_test.go` and `testdata/**` included (#220, kept open on purpose) | `compute-bumps.sh`, `cut-tags.sh` |
| `release-size.sh` | `decide_size` over `label_size` (the `release:*` labels of a merged pull request, through `pr_labels_of_commit` and `release_gh`/`release_repo`) and `text_ask` (the largest `Release-bump:` a message asks for, anywhere in it), ranked by `size_rank` | `cut-tags.sh`, `check-pr-size.sh` |

## Rules

- **`tag-format.sh` and `docs/site/scripts/lib/tag-format.mjs` are one
  contract** (ADR 0007 §1): the docs site's version dropdown parses the tags
  the release cuts, so the two regexes change in the same commit.
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
- **A hyphen is a pre-release only in the version.** `stable_tags` looks for it
  after the last `/`: `third-party/x-crypto/v0.18.0` is stable.

## Verify

```sh
make release-scripts-check    # the BATS suites of scripts/release/ exercise every function here
```
