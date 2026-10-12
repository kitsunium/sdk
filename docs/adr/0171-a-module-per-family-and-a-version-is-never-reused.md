# ADR 0171 — a module per pkg family, one shared internal module, and a published version is never reused

- **Status**: Accepted (the scripts and the release lock are built; the code split itself is the next change, in the order of §6)
- **Date**: 2026-10-12
- **Deciders**: SDK maintainers (the owner's decisions of 2026-10-12, recorded after one round of review)
- **Amends**: [ADR 0162](0162-the-sdk-is-one-module-and-a-release-is-one-tag.md) (§1 the SDK is one module; §3 tombstones; §4 what a release computes and tags), [ADR 0007](0007-sdk-release-and-versioning.md) (§1 the tag shapes; §2 the size is per module; §4 what a release tags), [ADR 0009](0009-pkg-public-module-resolvability.md) (resolvability is checked per module, and a version is checked against the proxy before it is cut), [ADR 0017](0017-pkg-bare-module-path.md) (the public surface is one module per family under `pkg/v1/`, not the bare `…/pkg`), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (§9 a connector is a module released on its own), [ADR 0156](0156-the-public-module-links-the-standard-library-and-nothing-else.md) (§1 the rule holds for each family module), [ADR 0157](0157-one-module-per-vendor-released-with-the-sdk.md) (§3 a vendor module is tagged at its own version, only when it changed)
- **Closes**: #295 (a vendor module tagged although it did not change), and the scope question of #220 (a test file cuts no release)
- **Related**: [ADR 0085](0085-both-halves-of-a-release-read-the-same-range.md) / [ADR 0089](0089-a-file-that-cannot-cut-a-release-cannot-size-one.md) / [ADR 0135](0135-a-release-is-sized-by-a-label-a-maintainer-set.md) (range, scope and size, now per module), [ADR 0137](0137-a-lane-that-loops-over-modules-reads-the-census.md) (the census), [ADR 0163](0163-the-sdk-is-designed-by-its-diagram-and-kit-writes-only-data-and-test-pins.md) / [ADR 0169](0169-the-sdk-is-rebuilt-from-its-design-in-a-copy-and-a-generation-is-held-to-the-whole-inline-set.md) (kit writes the SDK from `design/`)

## Context

ADR 0162 made the SDK one module and a release one tag. It solved what it set
out to solve — 346 tags and 18 per release for modules nobody bumped on their
own — and created the opposite problem: a consumer cannot hold one part of
the SDK still while taking a fix in another. Every `go get` of a fix in
`pkg/v1/net` moves `pkg/v1/data` with it, because they are one module at one
version. The owner's requirement, verbatim in the task: *"quand on utilise le
SDK on puisse importer n'importe quelle package dans une version locker sans
forcement tout mettre à jour si il y a un bug sur un package d'un côté mais
pas de l'autre"*.

Three facts constrain any answer, each measured on 2026-10-12.

**1. The proxy keeps every published version for good.** `@v/list` of every
path this repository ever published, from `proxy.golang.org`:

| module path | highest published | versions listed |
|---|---|---|
| `github.com/kitsunium/sdk` | v0.19.7 | 10 (v0.0.0, an unrelated 2024 history, then v0.18.0 … v0.19.7) |
| `…/pkg` | v0.18.0 (the tombstone) | 79 |
| `…/framework` | v0.18.0 (the tombstone) | 7 |
| `…/internal/kernel`, `…/internal/core`, `…/internal/service` | v0.17.0 | 78 each |
| `…/framework/connectors/{mysql,postgres,sqlite}` | v0.19.6 | 11 each |
| `…/framework/connectors/ssh`, `…/third-party/*` (nine modules) | v0.19.6 | 5 or 6 each |
| `…/internal`, `…/pkg/v1/<family>` (eleven families) | never published | `not found` |

A published module path at a published version is that content for every
consumer, and `sum.golang.org` seals it: re-cutting a version, or cutting a
version at or below the highest one, either fails verification or is
invisible to `@latest`.

**2. Every published root holds `pkg/v1/*` and `internal/*`.** A consumer
pinned to root v0.19.7 that adds a family module `…/pkg/v1/data` has two
modules providing `github.com/kitsunium/sdk/pkg/v1/data/...` — Go refuses with
`ambiguous import`. The same holds for `…/pkg` ≤ v0.17.0 and the three
internal modules ≤ v0.17.0, which ADR 0162's migration removes with
`…/internal/kernel@none`.

**3. `sdk-release.yml` was live.** v0.19.6 and v0.19.7 were cut automatically
on 2026-10-12, the day this decision was argued; any version the next merge
cut would have been one more number the proxy keeps.

## Decision

### 1. A module per pkg family, and one shared internal module

The target layout is one Go module per family under `pkg/v1/` —
`github.com/kitsunium/sdk/pkg/v1/<family>` for `app`, `clock`, `collections`,
`concur`, `crypto`, `data`, `errs`, `net`, `observe`, `proc`, `security` — and
ONE module `github.com/kitsunium/sdk/internal` holding `internal/kernel`,
`internal/core` and `internal/service`. Import paths do not change. A family
is the unit a consumer locks: `go get …/pkg/v1/net@v0.1.3` moves `net` and
whatever `net` requires, never `data`.

`internal` stays one module rather than following its domains: a module holds
its subtree, and `internal/core/<domain>` and `internal/service/<domain>` of
one family live under two different trees, so "the internal half of a family"
is no directory. It is also not published as a kernel of its own: what a
consumer may use of it is already aliased under `pkg/v1/{errs,clock,…}`.

Families that import each other in a cycle (`app` ↔ `security`, `app` ↔
`data`, measured by the challenge) are broken or merged in the split itself,
since a cycle of modules releases in lockstep anyway.

The vendor and connector modules stay as ADR 0157 and 0147 left them; the
framework stays in the root module.

### 2. A published version is never reused, nor gone below

**The invariant**: no module path is ever tagged at or below the highest
version the proxy lists for it. So the root continues above v0.19.7, the
framework above v0.18.0 if it ever becomes a module again, every vendor and
connector above v0.19.6, and the three retired internal paths above v0.17.0
if they are ever cut again. Only a path the proxy has never served — today
`…/internal` and the eleven `…/pkg/v1/<family>` — may start at v0.1.0.

It is checked, not remembered: `scripts/release/check-version-floor.sh`
reads `@v/list` from the proxy for every tag a release would cut and refuses
one at or below the published maximum (with a file mode for the tests and the
rehearsal). The release scripts already make the invariant the default — each
module continues its own last tag, and a module never tagged starts at v0.1.0
(§4) — and the floor check is what catches a tag a clone does not hold.

### 3. The root's next release is a tombstone of what it held, with a retraction

The first root release after the split, above v0.19.7, holds no `pkg/` and no
`internal/` — they are the family and internal modules now. Every family
module and the internal module require the root at that version or above, so
a consumer pinned to v0.19.7 who adds a family is lifted, by minimum version
selection, to a root that no longer provides the family's packages: no
ambiguous import, and no step the consumer has to know.

That root release's go.mod retracts the monolithic roots, `[v0.18.0,
v0.19.7]`, with a one-line rationale naming the family modules: `go list -m
-u` and `go get` then tell a consumer still on one of them where the packages
went. A retraction does not remove a version — a go.mod pinning one still
builds — it only stops `@latest` and `-u` from choosing it, which they would
not do anyway since it is lower.

**No git tag is deleted**, old or new: the tags are what the proxy and the
checksum database already hold, and `GOPROXY=direct` and ADR 0007's rollback
read them. GitHub releases of the old scheme may be removed later; that is
presentation, not publication, and not part of this change.

### 4. A release is per module: base, size, tag, and a manifest

What `scripts/release/` does, already built and tested before the code split
so the first split release runs on scripts that have been green for a while:

- **Which modules.** `compute-bumps.sh` prints the directory of every module
  `go.work` names that changed since ITS OWN last tag — measured from the
  first parent of that tag, over its own directory, the modules nested in it
  left out. The root's internal-only rule (rdeps) is kept for as long as
  `internal/` is the root's.
- **How big.** `cut-tags.sh` sizes each module from the `release:*` labels of
  the merges that changed IT (ADR 0135 per module): a `release:minor` on a
  pull request touching `pkg/v1/data` moves `data`, and not `net`.
- **Which version.** Each module continues its own last tag; a module never
  tagged starts at v0.1.0. Every in-repository requirement is pinned at the
  version this release gives that module, or at its own last tag.
- **Vendors only when they changed** (#295). A test file and a fixture are no
  change a consumer's build compiles — `*_test.go` and `testdata/` join the
  maintainer-only files of `lib/release-scope.sh` — so the regenerated pins
  that re-tagged all thirteen vendor modules at v0.19.0 now release nothing.
  This also settles #220's question: Go builds no test file of a dependency.
- **Tags.** `pkg/v1/<family>/vX.Y.Z` and `internal/vX.Y.Z` join the anchored
  shapes of `lib/tag-format.sh` and its docs-site mirror; every selection goes
  through an anchored shape, never a glob or a prefix (a prefix match read
  `pkg/v1/data/v0.1.0` as a tombstone in the release notes).
- **The push.** `git fetch --prune --prune-tags origin` before any tag is
  read; every module's last tag re-read before the publication; EACH exact ref
  of the release checked on origin right before tagging (`ls-remote` matches a
  pattern against a ref's tail, so the comparison is on full refs, the peeled
  `^{}` lines dropped); one release commit and one `--atomic` push.
- **Identity.** A release is several modules at several versions, so it is
  named by `release-manifest.json`: its first tag, every module with its path,
  tag, version, previous tag and size, the tombstones and the commits. The
  workflow refuses a run whose pushed tags the manifest does not name, and
  makes ONE GitHub release per run, on that first tag, with the manifest
  attached.

### 5. The release is locked until a maintainer opens it

`sdk-release.yml` cuts nothing while the repository variable
`RELEASE_ENABLED` is not `true`: an automatic run does not start, and a
dispatch with `dry_run=false` fails in its first step naming the variable. A
dispatch is a dry run by default — both scripts run, the tags and the
manifest it would cut are printed, nothing is pushed. A `pin/**` tag runs no
lane (#296).

### 6. The order of the bootstrap, with kit

The SDK is written by kit from `design/` (ADR 0163, 0169), and kit — like the
Studio, `kitsunium/platform` — is built on a PUBLISHED, older SDK. The loop is
held by pinning, never by an import: no module of this repository requires
`kitsunium/platform`. The split therefore lands in this order:

1. **kit N on the old SDK** reads a multi-module design (a module per layer
   or per directory, the rule that a module imports only the modules it
   declares, the facade and pins per module). Released by platform.
2. **The split SDK is generated by kit N** — go.work, the family and internal
   go.mods, `design/`, `MODULE.bazel`, gazelle and the layer checks —, the
   root tombstone of §3 and the first family and internal tags cut by
   dispatch, after a dry run, a floor check and a rehearsal on a file proxy.
3. **platform moves to the family modules**, pinned to the versions of step 2.
4. **kit N+1** is built on the split SDK.

Until step 1 exists, this repository does not split its code: the scripts and
the lock of §4 and §5 are what ships first.

## Consequences / Semantics

- A consumer locks per family; a fix in one family is one `go get` of that
  family and touches nothing else, except what that family requires.
- A release is no longer "the SDK at vX.Y.Z" but a manifest; the docs portal,
  which lists the root's releases (ADR 0007 §5), lists a release only when it
  cut the root. A family release appears in its GitHub release and manifest.
- `compute-bumps.sh`'s output contract changes from the one token `sdk` to
  module directories; `cut-tags.sh` still reads `sdk`, `pkg`, `framework`,
  `third-party` and `vN` as ADR 0162's release.
- Each module's numbering is its own; the vendor modules continue from
  v0.19.6, so their next tags are v0.19.7 or v0.20.0 while the root's is
  v0.19.8 or v0.20.0 — the equality of ADR 0162 was a coincidence of lockstep.

## Breaking changes

- None for a consumer until the split: the root keeps every package, and
  every version it published.
- At the split, a consumer pinned to a monolithic root who adds a family
  module is moved to the tombstone root by minimum version selection; one who
  still holds `…/pkg` ≤ v0.17.0 or an internal module ≤ v0.17.0 must first
  run ADR 0162's migration (`…/internal/kernel@none`).
- For maintainers: `compute-bumps.sh` prints module directories; a test-only
  change no longer cuts a release; releases need `RELEASE_ENABLED=true`.

## Alternatives considered

- **Reusing a published path at a lower version** (re-cutting `…/pkg` or the
  root from v0.1.0): refused — the proxy and the checksum database keep the old
  content, `@latest` never sees the lower one, and a consumer who fetched the
  old one cannot verify the new.
- **One module per domain in `internal/`**: refused — a module is a subtree,
  and a domain's core and service halves are in two subtrees (§1).
- **Deleting the tags of the old scheme**: refused — they are what
  `GOPROXY=direct` resolves and what ADR 0007's rollback reads; deleting one
  breaks those and changes nothing the proxy serves.
- **Splitting the code in this change**: refused for now — kit, which writes
  the SDK, does not read a multi-module design yet (§6), and a hand split would
  be overwritten by the next `kit gen`.
- **Leaving the release live while the scripts change**: refused — two
  versions were cut by it on the day of the decision (§5).

## Deferred

- The code split and the root tombstone (§1, §3): after kit N exists (§6).
- Tombstones for `…/internal/{kernel,core,service}` above v0.17.0: only if the
  file-proxy rehearsal shows a consumer reaching them; ADR 0162's migration
  removes them today.
- Deleting the GitHub releases of the old scheme (presentation only; no tag).
- A docs-portal axis per family module (it lists root releases only).
- A tag ruleset protecting release tags needs a GitHub App as the bypass actor
  (`GITHUB_TOKEN` is not one); out of this change.

## References

- `scripts/release/` — `compute-bumps.sh`, `cut-tags.sh`, `release-notes.sh`,
  `check-version-floor.sh`, `proxy-rehearsal.sh`, and `lib/` (`tag-format.sh`,
  `release-modules.sh`, `release-scope.sh`); their CLAUDE.md files.
- `.github/workflows/sdk-release.yml` — the lock and the dry run.
- Go modules reference — minimum version selection, `retract`, ambiguous
  imports: <https://go.dev/ref/mod>.
- Issues #295, #220, #271, #217, #296 on `kitsunium/sdk`.
