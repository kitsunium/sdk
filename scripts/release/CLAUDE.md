<!-- updated: 2026-10-04T01:25:00Z -->
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
| `compute-bumps.sh` | prints, one per line, the directory of every module `go.work` names (`lib/release-modules.sh`) that changed since its OWN last tag — `.` the SDK module at the root, `pkg/v1/<family>`, `internal`, `third-party/<path>`, `framework/connectors/<engine>` (ADR 0171) —, each measured from the first parent of its own last tag (a module never tagged: from the empty tree) over its own directory, the modules nested in it left out. The root releases on a path under `pkg/` or `framework/` of its own, its `go.mod`, `LICENSE` or `README.md`, or a vendor directory no module claims (rule 1), and on an `internal/` change only when its Bazel reverse dependencies reach `//pkg/...`, `//framework/...` or `//third-party/...` (rule 2); any other module on any path of it that counts (rule 1m). Maintainer-only files — and since ADR 0171 `*_test.go` and `testdata/` (#295) — are filtered out first (`lib/release-scope.sh`). `--explain` writes, per module, the range, the rule and the verdict to stderr; `--require-bazel` refuses to decide rule 2 without Bazel; `--range` measures every module over one range | `sdk-release.yml`; `make release-dry-run` with `--dry-run` |
| `cut-tags.sh` | reads the modules on stdin (compute-bumps.sh's directories; `sdk` — and `pkg`, `framework`, `third-party`, `vN` — is the SDK module plus every module that changed since its own last tag, measured here; a directory `go.work` does not name is refused). Publishing, it first runs `git fetch --prune --prune-tags origin`. Each module's version continues its OWN last tag, sized by the largest `release:*` label on the merged pull requests behind the first-parent commits of ITS range that changed a path of it (one GitHub lookup per commit, cached across modules), or by `--bump` for every module; a module never tagged starts at `v0.1.0`. The root keeps ADR 0162's rules: it continues the newest `pkg/vX.Y.Z` before its first tag, which is held (exit 3) without `--allow-bootstrap`, refused as a patch (exit 65), and cuts the tombstones `pkg/vX.Y.Z` and `framework/vX.Y.Z`. Every module tagged gets a go.mod requiring each module of the repository at the version this release tags it at, or at its own last tag, with no `replace`, on ONE detached release commit — empty when nothing is rewritten; every module's last tag is re-read before the publication and EACH exact ref checked on origin right before tagging (`remote_tags_present`, exit 2 on a race); one `--atomic` push. The published go.mod of `internal` and of every `pkg/v1/<family>` also requires the root at its FLOOR (ADR 0171 §7, `root_floor`): the lowest root tag whose tree holds `internal/go.mod`, else the version this release gives the root; a family or `internal` cut while no root holds the split is refused (exit 1) — the floor is what lifts a consumer pinned to a monolithic root past the ambiguous import, and it is written here because the working tree's go.mods cannot carry it (the root requires them back, a cycle kit refuses). `--no-push` does everything a publishing run does but the push — the release commit and the tags stay in the clone, for `proxy-rehearsal.sh --manifest` —; with `--dry-run` it is refused (exit 64). `--manifest=<file>` writes `release-manifest.json` (the release's identity — its first tag —, each module's directory, path, tag, version, previous tag and size, the tombstones, the commits, `pushed` and `root_floor`), in a dry run too. stdout: the tags, in `go.work`'s order (the SDK's first when cut), then the tombstones | `sdk-release.yml`; `make release-dry-run` with `--dry-run` |
| `check-pr-size.sh` | `--pr=<number>`: the size the squash of that pull request would publish, over every commit of the branch; exit 1 with the reason and the fix when no label decides it, 2 when GitHub cannot be read, 64 on bad usage | `release-size.yml`, on every push and label change of a pull request |
| `check-version-floor.sh` | ADR 0171 §2 against the module proxy, never the clone: with no argument, every module `go.work` names must have a last tag here at or above the highest version the proxy lists for it, and a module never tagged here must be a path the proxy never served; with tags (arguments, `-` for stdin, or `--manifest=`), each must be strictly above its module's published maximum (a pre-release below its release). `--proxy=` (or `VERSION_FLOOR_PROXY`, default `https://proxy.golang.org`) takes a `file://` directory in GOPROXY's layout. A 404/410 is "never published"; any other answer is exit 2. Exit 1 names each module below its floor; nothing checked is a failure | `sdk-release.yml` (before the cut, dry run included) |
| `proxy-rehearsal.sh` | a release rehearsed on a `GOPROXY=file://` directory, offline, the zips written by a stdlib-only Go program it builds: `--fixture` (default) — a monolithic root v0.19.7 holding `pkg/v1/data`, a root v0.20.0 holding neither `pkg/` nor `internal/` and retracting `[v0.18.0, v0.19.7]`, a family `pkg/v1/data` v0.1.0 requiring it — a consumer pinned to v0.19.7 adds the family and must build with the root lifted to v0.20.0, and the control (the family NOT requiring the new root) must fail with `ambiguous import`; `--manifest=<file> --ref=<commit>` — every module of a manifest zipped from the release commit beside the published v0.19.7, one consumer per family module, each of which must build with the root lifted from v0.19.7 to the manifest's `root_floor` and a tidied go.mod naming no monolithic root, and a control — the first family re-published without the floor — that must fail with `ambiguous import`; exit 3 when the manifest names no family or no floor. A published zip is fetched with `curl -L`: the proxy answers it with a redirect to its storage. `KEEP=1` keeps the work directory | by hand; the suite runs the fixture |
| `release-notes.sh` | `classify`, `count` or `notes` over the tag list cut-tags.sh printed: each tag's family by `lib/tag-format.sh`'s anchored shapes (sdk, vendor, tombstone), refusing a tag none accepts and an empty list; `notes` is the Markdown of the one GitHub release | `sdk-release.yml` (the published-tag count, the release notes) |
| `lib/` | the shared rules — see `lib/CLAUDE.md` | sourced by the scripts above |
| `test-compute-bumps.bats`, `test-cut-tags.bats`, `test-check-pr-size.bats` | the regression suites: each builds a throwaway repository and asserts the tag the scripts compute — and the publish path pushes to a bare repository the test owns, so the release commit and the tags on it are asserted too, the tombstones' trees and go.mods among them — byte for byte, and the deprecation notice as Go reads it —, and a `git` shim (`log_pushes`) counts the pushes a release makes; GitHub is a stub (`test-helpers.bash`) that runs the caller's own `jq` filter over fixtures and logs every call | `release-scripts-test.sh` |
| `test-modules.bats` | the release of several modules over the layout of ADR 0171 (the root, `internal`, two families, a vendor): compute-bumps naming the one module that changed, each from its own tag; a label on one family's merge sizing that family only, one lookup for a merge touching two; each version continuing its own tag; a family pinning `internal` at its new version or its last tag; `sdk` keeping a vendor whose only change is a test (#295); the atomic push on one release commit; the prune-fetch dropping a stray local tag; the exact-ref race guard (a git shim tags origin between the fetch and the push) and its immunity to a tail match; the manifest in a dry run and for real; the floor on the root (the first split release has `internal` and every family require the root it cuts, a family cut while no root holds the split is refused, a later release keeps the first root that held it); `--no-push` tagging here and pushing nothing; a commit whose paths git cannot read refusing the size (cut-tags.sh:207); an odd path counting once; `release_base` over several root commits | `release-scripts-test.sh` |
| `test-floor-rehearsal.bats` | `check-version-floor.sh` against a file proxy: above, at, below and a pre-release of the maximum; a never-published path at v0.1.0 and a published one refused there; the default question over a clone behind the proxy and a module never tagged here that the proxy serves; a manifest's tags and tombstones; an unreachable proxy (exit 2); a tag no release cuts. And `proxy-rehearsal.sh`: the fixture green with its control red, a manifest with no family exit 3 | `release-scripts-test.sh` |
| `test-regressions.bats` | #271 and #217 in the shapes they shipped in, through `compute-bumps.sh \| cut-tags.sh` as an automatic run pipes them: a `release:minor` label on a framework/-only squash merge cuts the minor (it shipped v0.14.1 while `release-scope.sh` counted `pkg/` and `internal/` only — closed by #283, not by ADR 0135); a `Release-bump: minor` at line 99 of a 126-line message, unlabelled, is refused (exit 65) and, labelled, cuts the minor (closed by ADR 0135). Each was seen red with its defect reintroduced into the current libraries | `release-scripts-test.sh` |
| `test-tag-format.bats` | the module shapes of ADR 0171 (`pkg/v1/<family>/vX.Y.Z`, `internal/vX.Y.Z`) anchored at both ends, neither pkg's history nor a tombstone; `module_tag_dir`, `is_module_dir`, `latest_module_tag` reading one module's tags exactly; `remote_tags_present` against a bare remote holding annotated tags (full refs, no `^{}`) and one it cannot read | `release-scripts-test.sh` |
| `test-release-workflow.bats` | what `sdk-release.yml` decides around the scripts, read from the file: the job's `if` needs `RELEASE_ENABLED == 'true'` for an automatic run, `dry_run` defaults to true, a real dispatch is refused while locked, the dry run passes `--dry-run` and gates the publishing steps; and `release-notes.sh`'s classification | `release-scripts-test.sh` |
| `test-helpers.bash` | `install_gh_stub` and the fixtures' helpers, loaded with `load test-helpers`; `.bash` so the runner's `*.bats` glob never runs it alone | — |
| `release-scripts-test.sh` | runs every `*.bats` here by glob, never a list; refuses to run zero suites; exits 127 naming the install routes when `bats` is not on `PATH` (it is never fetched) | `make release-scripts-check`, in `bazel-ci.yml`'s `shell-gates` job |
| `test-sync-versions.mjs.test.js` | a Node test of the docs site's `docs/site/scripts/lib/tag-format.mjs` (the tag regex, parsing, grouping, `versions.json`) | no lane — `docs/site`'s `npm test` globs `scripts/lib/*.test.mjs` only |

## Rules

- **The size is a label, never text.** A `release:*` label a maintainer set on
  the merged pull request decides; a `Release-bump:` line in the message is only
  read so that one asking for more than a patch with no label to decide it is
  refused loudly, before anything is published (#217, #224).
- **A module per release question, at its own version** (ADR 0171, amending
  ADR 0162). Every module `go.work` names (`lib/release-modules.sh`
  `release_modules`, which refuses `./pkg`, `./framework` and a layer of
  `internal/`) is measured from the first parent of its OWN last tag, over its
  own directory with the modules nested in it left out, and released only when
  a path of it that counts changed — so a family's change does not tag the
  root, and a vendor whose only change is a test keeps its tag (#295). It is
  sized by the merges that changed IT: a label on one family's pull request
  moves that family alone. Its version continues its own last tag; a module
  never tagged starts at `v0.1.0`, which only a path the proxy has never served
  may do (`check-version-floor.sh`). A module that keeps its tag keeps a go.mod
  requiring the versions it was cut with.
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
- **The tombstones, once** (ADR 0162). The first root tag after the pkg
  history — and no other release — also cuts `pkg/vX.Y.Z` and
  `framework/vX.Y.Z` at its version: on a child of the release commit, the
  module directory reduced to a go.mod (a one-line `// Deprecated:` notice on
  the module directive — `tombstone_deprecation`: moved into
  `github.com/kitsunium/sdk` at vX.Y.Z, and the migration command —, its own
  module path, the SDK's `go` directive, `require github.com/kitsunium/sdk
  vX.Y.Z`), the modules `go.work` names beneath it kept whole. Without them Go
  resolves an import of `…/pkg/v1/…` or `…/framework/…` to those modules at
  v0.17.0 for good, and a new consumer's bare `go mod tidy` lands there
  (measured). Go reads a module's deprecation from its latest go.mod, so
  `go list -m -u` marks both `(deprecated)` for a consumer still on them and
  `go get` prints the notice — its first line alone, none of it past 500
  bytes, hence one line (measured, ADR 0162 §As built). They are built with
  plumbing — no hook, no worktree change —, checked (`assert_tombstone`: the
  notice as Go reads it, one line of at most 500 bytes, then the module path,
  the `go` directive and the one requirement alone; `assert_tombstone_tree`)
  before they are tagged, and pushed in the root tag's atomic push. A
  tombstone tag that already exists is refused (exit 1) before anything is
  tagged, before the hold too. A tombstone is no pkg
  release: `latest_pkg_tag` skips a tag whose `pkg/` holds a go.mod alone, and
  `latest_release_tag` takes the root tag at a shared version — otherwise the
  next release would read `pkg/v0.18.0` as its base and be the first root tag
  again. Never for `internal/*`: nothing imports them.
- **A release commit always exists.** It is the tags' commit, a detached child
  of the main commit the release was cut from — `--allow-empty` when nothing is
  rewritten —, because the next range and every vendor module's next
  measurement start at a tag's first parent.
- **The publication subshell honours errexit.** It runs under its own
  `set -e` and its status is read after it: under `( … ) || rc=$?` bash ignored
  errexit inside, and a refused go.mod was released anyway.
- **The range walk is `--first-parent`**, so a commit a merge brought in never
  sizes a release, and each commit's paths are a DIFF against its first parent
  (`commit_touches_module`): `git log -1 <sha> -- <path>` answers the newest
  commit at or below <sha> that touched the path, which sized a family by
  another family's merge (seen red first).
- **The tags read are origin's, and each one pushed is checked there.** A
  publishing run fetches with `--prune --prune-tags` before it reads a tag,
  and checks every exact ref of the release on origin right before it tags:
  `ls-remote` matches a pattern against a ref's tail, so the comparison is on
  full refs (`remote_tags_present`). A git that fails anywhere on that path is
  a refusal, never "nothing changed" or "nobody tagged it".
- **A release has an identity: its manifest.** `release-manifest.json` names
  the first tag (the one GitHub release), every module and version, the
  tombstones and the commits; the workflow refuses a run whose pushed tags it
  does not name.
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

A real rehearsal, in a throwaway clone (the tags `--no-push` makes stay in it):

```sh
git clone --no-local . /tmp/rehearsal && cd /tmp/rehearsal
echo sdk | bash scripts/release/cut-tags.sh --no-push --bump=minor --manifest=../release-manifest.json
bash scripts/release/check-version-floor.sh --manifest=../release-manifest.json
bash scripts/release/proxy-rehearsal.sh --manifest=../release-manifest.json \
  --ref="$(jq -r .release_commit ../release-manifest.json)"
```
