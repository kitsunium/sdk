# ADR 0171 — a module per pkg family, one shared internal module, and a published version is never reused

- **Status**: Accepted (the scripts and the release lock are built; §7 measures the split and breaks its cycles; the code split itself is the next change, in the order of §6)
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

### 7. The split, as measured

Step 1 of §6 exists: kit `v0.1.0-rc.8`, the version `design/sdk.yaml` pins,
reads a library's `modules:` (platform ADR 0011) — each module by the
directory of its go.mod and the modules it may require, every cycle of those
requirements refused when the design is read, and `kit check` holding every
go.mod, its require directives and every import, a test's included, to them.

**The graph, measured on 2026-10-12** — every package of the workspace, its
imports, its test imports and its external test imports, the union over ten
cells (`linux`, `windows`, `darwin`, `freebsd`, `openbsd`, `netbsd`,
`illumos`, `solaris`, `js`, `wasip1`), each package mapped to the module that
would hold it:

```sh
for os in linux windows darwin freebsd openbsd netbsd illumos solaris; do
  GOOS=$os go list -e -f \
    '{{.ImportPath}}|{{join .Imports " "}}|{{join .TestImports " "}}|{{join .XTestImports " "}}' ./...
done | sort -u   # then: internal/** → internal, pkg/v1/<f>/** → pkg/v1/<f>, framework/** → .
```

| module | packages | production imports of another module |
|---|---|---|
| `internal` (kernel 18, core 72, service 104) | 194 | none |
| `pkg/v1/data` | 28 | `internal` 76, `errs` 1 |
| `pkg/v1/app` | 15 | `internal` 39 |
| `pkg/v1/proc` | 11 | `internal` 38, `errs` 1 |
| `pkg/v1/crypto` | 7 | `internal` 20 |
| `pkg/v1/concur` | 6 | `internal` 6, `clock` 1 |
| `pkg/v1/net` | 6 | `internal` 12 |
| `pkg/v1/observe` | 6 | `internal` 28 |
| `pkg/v1/security` | 5 | `internal` 16, `errs` 2, `crypto` 1 |
| `pkg/v1/collections` | 2 | `internal` 2 |
| `pkg/v1/clock`, `pkg/v1/errs` | 1 each | `internal` 1 |
| `.` — the framework | 25 | `internal` 15 and ten families |

**The production graph has no cycle.** Five imports cross from one family to
another (`concur → clock`, `data → errs`, `proc → errs`, `security → errs`,
`security → crypto`); everything else a family imports is `internal`.

**The tests made one strongly connected component of eight families** —
`app`, `clock`, `crypto`, `data`, `errs`, `observe`, `proc`, `security` —
through six cycles, each of them test-only: `errs ↔ app` and `errs ↔
observe` (an `errs` suite drew its typed failures from `observe/logger` and
`app/mail`), `app ↔ data` (four codec suites read their format through
`app/config`), `app ↔ security` (`security/secret`'s suite loaded a
configuration), `observe ↔ security` (`slogbridge`'s suite rendered a
`secret.Value`) and `clock ↔ app/data/security` (ADR 0090's nine
configurations). A cycle of modules releases in lockstep (§1), and kit refuses
one, so each was **broken, none merged** — the direction kept is the one the
production code already has, and a test moves up to the module that may see
both sides or names the engine its facade forwards to:

| suite | was in | now |
|---|---|---|
| `TestV1ErrsEndToEnd`, `TestHasAnyCode_AndHasAnyReason` | `pkg/v1/errs` | `pkg/v1/observe/logger` (the failure is the logger's own code, `1.1.0.1`) |
| `TestFieldsOfReadsTheClauseAnEmitterAttached` | `pkg/v1/errs` | `pkg/v1/app/mail` |
| ADR 0090's three cross-family tests and their doubles | `pkg/v1/clock` | `pkg/v1/app/scheduler` — still public names only |
| `TestAConfigurationFieldNeverLeaks` | `pkg/v1/security/secret` | `pkg/v1/app/config` |
| `TestASecretRendersItsPlaceholderThroughSlog` | `slogbridge` | stays; names `secret.Value` through `internal/core/security/secret`, the type the facade aliases (`log/slog` is `slogbridge`'s alone, ADR 0032) |
| `TestItReadsItsFormatThroughTheRegistry` (json, bson, toml, yaml) | `pkg/v1/data/codec/*` | stays; reads through `internal/service/app/config`, the engine `pkg/v1/app/config` forwards to — so each suite still links its one codec |

After it the graph — tests included — is a DAG, in this order:
`errs`, `clock` < `collections`, `crypto`, `net`, `proc`, `concur` <
`observe` < `data` < `security` < `app` < `.`. **Nothing is merged**: no
strongly connected component of more than one module is left.

**The modules.** Twelve where the SDK module was — `internal` and the
eleven families — plus the root, which keeps the framework (§1), and the
thirteen vendor and connector modules (ADR 0157, 0147). A module's
`requires:` is every module of the repository its go.mod names, because the
go command lists in a go.mod every module that provides a package to its
build and its tests, and kit holds every require directive to the design
(platform ADR 0011 §5):

```yaml
modules:
  - dir: .
    requires: [internal, pkg/v1/app, pkg/v1/clock, pkg/v1/concur, pkg/v1/crypto,
               pkg/v1/data, pkg/v1/errs, pkg/v1/net, pkg/v1/observe, pkg/v1/proc,
               pkg/v1/security]
  - dir: internal
  - dir: pkg/v1/errs
    requires: [internal]
  - dir: pkg/v1/clock
    requires: [internal]
  - dir: pkg/v1/collections
    requires: [internal, pkg/v1/errs]
  - dir: pkg/v1/concur
    requires: [internal, pkg/v1/clock, pkg/v1/errs]
  - dir: pkg/v1/crypto
    requires: [internal, pkg/v1/errs]
  - dir: pkg/v1/net
    requires: [internal]
  - dir: pkg/v1/proc
    requires: [internal, pkg/v1/errs]
  - dir: pkg/v1/observe
    requires: [internal, pkg/v1/clock, pkg/v1/errs]
  - dir: pkg/v1/data
    requires: [internal, pkg/v1/clock, pkg/v1/errs, pkg/v1/proc]
  - dir: pkg/v1/security
    requires: [internal, pkg/v1/clock, pkg/v1/crypto, pkg/v1/errs,
               pkg/v1/observe, pkg/v1/proc]
  - dir: pkg/v1/app
    requires: [internal, pkg/v1/clock, pkg/v1/crypto, pkg/v1/data,
               pkg/v1/errs, pkg/v1/observe, pkg/v1/proc, pkg/v1/security]
  # the nine third-party modules: [internal]; the four connectors: the root,
  # the families they import and their closure — as their go.mod lists them.
```

`errs` and `clock` hold one package each and are kept apart all the same:
they are what every other family requires, so folding either into a larger
module would make a fix there move every family that requires it.

**The floor of §3 is written by the release, not by the tree.** §3 has every
family and `internal` require the root at the first version that holds
neither `pkg/` nor `internal/`, and the root requires them back because the
framework imports them — a cycle kit refuses, and one with no import in it:
the family requires the root only so that minimum version selection lifts a
consumer off a monolithic root. So the working tree's go.mods do not carry
it, and `cut-tags.sh` writes it into the published go.mod of `internal` and
of every family, beside the versions it pins, at the root's earliest tag whose
tree holds no `pkg/` — or at the version the same release gives the root,
when that release is the first. A family cannot be cut before that tag
exists or is cut with it.

**Bazel holds the split, measured on a spike before it was made**: three
modules (`internal`, `pkg/v1/errs`, `pkg/v1/clock`) added to `go.work`, each
with a `replace` onto its directory, the root requiring them. `go_deps.
from_file(go_work)` read them once each module that requires another has a
`go.sum` — an empty one, which `go mod tidy` keeps — and `internal/` a
`BUILD.bazel` marking the module root, as the vendor modules have; `bazel mod
tidy` added one `use_repo` entry per new module (as it lists
`com_github_kitsunium_sdk` today) and left `MODULE.bazel.lock` byte for byte;
`bazel run //:gazelle` changed no `BUILD.bazel`, because every label still
resolves inside the one Bazel repository by import path; `bazel build //...`
(1 108 targets), the `errs` audit and `check-layer-deps.sh` passed unchanged.
What does not survive the split is the per-module `GOWORK=off` build of a
module whose go.mod lacks a `replace` for every repository module in its
closure — a `replace` in a dependency's go.mod is ignored — so every go.mod
carries one per module it requires, directly or not, and the release drops
them all (`rewrite_publishable`). `kit check` runs the `modules` rule and, as
expected, refuses `docs/api` until `make api` writes one file per module.

**The root keeps the framework and is not deprecated.** A deprecation is the
whole module path's, read from its latest go.mod, and the root still holds
`framework/`; marking it would tell every framework consumer to leave a module
they are meant to stay on. What tells a consumer on a monolithic root where
the packages went is the retraction's rationale, which `go get` and
`go list -m -u` print beside the retracted version.

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
