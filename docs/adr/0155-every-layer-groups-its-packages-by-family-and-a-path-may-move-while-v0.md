# ADR 0155 — every layer groups its packages by family, at the depth the families need, and an import path may move while v0

- **Status**: Accepted
- **Date**: 2026-10-03
- **Deciders**: SDK maintainers
- **Amends**: [ADR 0013](0013-sdk-crypto-domain.md) and [ADR 0014](0014-sdk-transform-crypto-ports-config-topology.md) (the scheme facades placed beside `pkg/v1/crypto`), [ADR 0102](0102-a-document-somebody-else-wrote-is-decoded-one-way-or-not-at-all.md) §D5 (the reason it gave for that placement), [ADR 0016](0016-sdk-process-supervision-domain.md) (six `proc` facades at the root of `pkg/v1`), [ADR 0029](0029-sdk-net-domain.md) (three `net` facades at the root of `pkg/v1`), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (its v0 licence now covers import paths)
- **Related**: [ADR 0001](0001-sdk-go-multimodule-layout.md) (the layers this groups inside), [ADR 0035](0035-pp-range-ownership-enforcement.md) (the owner table keyed on directories), [ADR 0068](0068-layer-firewall-is-a-checked-graph.md) (the firewall keyed on layers), [ADR 0134](0134-a-program-links-the-codecs-it-imports.md) (one facade per format), [ADR 0135](0135-a-release-is-sized-by-a-label-a-maintainer-set.md) (the size label), [ADR 0147](0147-the-framework-is-a-module-of-the-sdk-above-pkg.md) (the framework moves in lockstep), [ADR 0154](0154-the-sdks-principles-are-one-charter-and-an-incidents-rule-lives-with-its-code.md) (principle 4), [ADR 0158](0158-distribution-mechanisms-are-the-frameworks-not-the-sdks.md) (the four packages that leave), [ADR 0159](0159-the-kernel-holds-what-the-domains-rewrote-and-is-published-by-nature.md) (`concur` and `collections`), [ADR 0160](0160-every-service-has-a-core-and-a-code-keeps-its-value-when-it-moves.md) (core mirrors service)

## Context

`pkg/v1` has 55 top-level directories for 66 packages, and no rule decides
whether a package is a child or a sibling. `server/sse`, `server/websocket`
and `server/static` are children of `server`, while `client` and `tlsid`, the
same domain's other two faces, are siblings of it. `codec/json`, `codec/yaml`
and `codec/strictjson` are children of `codec`, while `hash`, `kdf`, `mac`,
`sign`, `agree` and `password` are siblings of `crypto`. `proc` holds only a
capability matrix, while its eight facades (`process`, `signal`, `reaper`,
`rlimit`, `cgroup`, `memlimit`, `sdnotify`, `sdlisten`) sit beside it.
`internal/core` is flat, 35 directories; `internal/service` groups some
domains (`codec/*`, `crypto/*`, `net/*`, `proc/*`) and not others.

The one reason ever written for the flatness is wrong. ADR 0102 §D5 placed
`strictjson` under `pkg/v1/codec` and gave as its precedent "the reason
`pkg/v1/hash` and `pkg/v1/sign` stand apart from `pkg/v1/crypto`" — so that
importing one does not link the other; the root `CLAUDE.md` still says the
crypto facades are "siblings, not children". In Go, importing a package links
that package and what it imports, and never the package of its parent
directory. Measured on this tree: `go list -deps ./pkg/v1/codec/strictjson`
contains neither `pkg/v1/codec` nor any of the codecs `pkg/v1/codec`
blank-imports, and `./pkg/v1/server/websocket` does not contain
`pkg/v1/server`. ADR 0102 relied on exactly that when it put `strictjson`
UNDER the umbrella it must not link. Nesting is link-neutral; the sibling rule
bought nothing and cost a flat public tree.

The families already exist. The service dependency graph clusters into
persistence, security, runtime, observability, messaging and data-shape groups,
and a reader looking for "the thing that signs" or "the thing that writes
metrics" thinks in those groups, not in an alphabetical list of 55.

ADR 0040 permits a published SHAPE to change while the module is v0. It says
nothing of an import PATH, and a path cannot be changed compatibly in Go: there
is no package redirect, so a moved package breaks every importer at compile
time.

## Decision

### 1. One set of families, the same in every layer

| Family | Domains | Notes |
|---|---|---|
| (root) | `errs`, `clock` | used by every family and owned by none; `semver` joins them in `pkg/v1` (ADR 0159) |
| `concur` | `group`, `singleflight`, `worker`, `batcher`, `snapshot`, `recycler` | kernel primitives, published by alias (ADR 0159) |
| `collections` | `heap`, `ring` | kernel primitives, published by alias (ADR 0159) |
| `crypto` | the AEAD at the family's root; `hash`, `kdf`, `mac`, `sign`, `agree`, `password` as its children | the service engines grouped by role: `aead/{aesgcm,streamaead}`, `sign/{ed25519sig,ecdsasig}`, `mac/hmacsha2`, `kdf/{hkdfsha256,keytree}`, `hash/stdhash`, `agree/x25519`, `password/{pbkdf2pw,commonpw}`, `key/{jwk,keyenvelope}` |
| `security` | `secret`, `redact`, `token`, `session`, `authz` | |
| `net` | `server`, `client`, `tlsid`, `sse`, `websocket`, `static` | the family root holds the domain's ports, as `internal/core/net` does today |
| `proc` | `process`, `signal`, `reaper`, `rlimit`, `cgroup`, `memlimit`, `systemd/{notify,listen}`, `ipc` | the capability matrix stays at the family's root |
| `observe` | `log` (the logger, its writers and sinks), `metrics`, `trace`, `profiling` | plus the OpenTelemetry model `metrics` and `trace` share, and their OTLP emitter, internal |
| `data` | `codec`, `transform`, `sql`, `docstore`, `queue`, `cache`, `vfs` | every codec in one tree (§3) |
| `app` | `config`, `cli`, `i18n`, `validation`, `view`, `events`, `scheduler`, `statemachine`, `resilience`, `lifecycle`, `health`, `lock`, `id`, `mail` | |

There is no distribution family: `entitlement`, `selfupdate`, `gate` and `git`
leave the SDK for the framework (ADR 0158).

### 2. The same path in every layer, at the depth the families need

A domain lives at `<family>/<domain>` in `internal/core`, `internal/service`
and `pkg/v1` alike — `internal/core/observe/metrics`,
`internal/service/observe/metrics`, `pkg/v1/observe/metrics` — and a service
engine may nest below its domain (`internal/service/crypto/sign/ed25519sig`).
The kernel takes a family where one applies: the primitives `pkg/v1` publishes
under `concur` and `collections` sit under the same paths in the kernel; the
kernel packages no family describes (`errs`, `clock`, `cache`, `buffer`,
`pathchain`, `plugin`, and ADR 0159's `backoff`, `flock`, `semver`) stay at its
root.

There is no depth limit. A directory exists when it names something its
members share, and not to reach a depth; a family directory that holds no Go
code carries a `CLAUDE.md` that lists its members. Leaves keep their names
unless the family makes the name redundant or wrong: `sdnotify` and `sdlisten`
become `systemd/notify` and `systemd/listen`, the logger's directory is `log`
under `observe`, and `vcs` is `git` in every layer for as long as it is in the
SDK. A leaf that shares its name with a standard-library package (`signal`,
`hash`, `sql`, `log`) is accepted: the path tells them apart, and a file that
needs both names one of them.

### 3. Every codec in one tree

Every package that reads or writes a wire format for its own sake lives under
`data/codec`: the sixteen format packages and their 24 Formats, the per-format
facades of ADR 0134, and the JSON tools that are not Formats — strict decoding,
a type's wire shape, the difference of two documents (`strictjson`,
`jsonshape`, `jsonpatch`, which ADR 0102, 0133 and 0143 already placed in the
codec tree). A draft that gave the JSON tools a `data/json` family of their own
is not taken: it splits the tree a reader searches for "decode this".
`transform` is not a codec — it transforms bytes, it does not map values — and
stays a sibling of `codec` in `data`. A wire format that exists to serve one
domain — WebSocket frames, `traceparent`, a language tag — is not a codec
either: it stays with that domain, in its service (ADR 0160).

### 4. An import path may move while v0 — ADR 0040, extended

A package's import path is part of the published surface, and while the module
is v0 it may change on the same terms ADR 0040 gives a shape: loudly, and only
while v0. A move is a clean break:

- **no alias package** is left at the old path and there is no deprecation
  window — it would double the surface for consumers nobody can name, under a
  version that promises nothing;
- the pull request names every path it moves, old and new, and carries the
  `release:minor` label (ADR 0135);
- the framework's imports move in the same pull request, because it is
  released in lockstep with `pkg` (ADR 0147);
- past `pkg/v1.0.0` a path is frozen exactly as a shape is.

### 5. One family per pull request, renames only

A move is `git mv` plus a path substitution, regenerated `BUILD.bazel` files
and regenerated READMEs, and nothing else: never in the same pull request as a
change of logic, and never across a module boundary. The tables kept by hand
move in the same pull request — `codeRangeOwners` (its keys are code values and
do not change; its values are directories, ADR 0160), the `//:audit_sources`
labels, `tools/alloc-lane-targets.txt`, the `.ktn-linter.yaml` globs (a stale
glob fails nothing, so they are compared with `find` by hand), each facade's
`//go:generate gomarkdoc --repository.path`, the docs site's feature anchors,
the logger's `x_defs`, the paths in `tools/sdkguard`'s rules and the framework's
path literals.

### 6. The documentation guard follows a nested core

`scripts/pre-commit/check-domain-docs.sh` compares the root `CLAUDE.md`
architecture tree's `core/` list with `find internal/core -maxdepth 1`. Once
core nests, it compares the tree with every directory under `internal/core`
that holds Go code, as a path relative to `internal/core` — still keyed on what
is on disk, never on a number someone maintains.

## Consequences / Semantics

- **Implemented by the reorganisation series**, one family per pull request:
  the moves in `internal/core`, `internal/service` and `pkg/v1`, the
  `concur` / `collections` packages, and the guard of §6. This record changes
  no code; the tree on disk is the authority, and §As built records where it
  settled a detail the table above left open or departed from it.
- **As implemented in the kernel**, which took a family wherever one applies
  rather than only where `pkg/v1` publishes one: `buffer` joined `concur`
  beside the `recycler` it specialises, `cache` joined `collections`, and
  `pathchain` heads a third family, `fs`, which ADR 0159's `flock` and the
  Windows DACL reader `winacl` joined when they left `lock`; `errs`, `clock`,
  `backoff`, `semver` and `plugin` stay at its root (ADR 0159, as
  implemented).
- What does not change: the four layers and their direction
  (`check-layer-deps.sh` is keyed on layers, not on directories), every error
  code value (ADR 0160), the module boundaries, and the standard-library-only
  status of the kernel and the core.
- Every `pkg/v1` import path changes once, except the four already at their
  family's path — `errs`, `clock`, and `crypto` and `proc`, which become family
  roots. A consumer updates its imports once, mechanically, at the minor
  release that moves them.
- ADRs keep the paths they were written with: they are records, and a path in
  an ADR is the path of its time. Only enforcement artefacts and `CLAUDE.md`
  files move with the code.

## Breaking changes

Every import path under `pkg/v1` except `errs`, `clock`, `crypto` and `proc`,
once per family, with no alias left behind — permitted only because the module is v0, under §4.
No shape, no behaviour and no error code changes.

## As built

Measured from disk on the series' integrated tree: a package is a directory
holding a non-test `.go` file, counted per layer.

| Layer | Packages | At its root | Families |
|---|---|---|---|
| `internal/kernel` | 18 | `backoff`, `clock`, `errs`, `plugin`, `semver` | `concur` 7 (`batcher`, `buffer`, `group`, `recycler`, `singleflight`, `snapshot`, `worker`), `collections` 3 (`cache`, `heap`, `ring`), `fs` 3 (`flock`, `pathchain`, `winacl`) |
| `internal/core` | 72 | — | `crypto` 2 (the family's root, `key/jwk`), `security` 5, `net` 1 (the family's root), `proc` 2 (the family's root, `ipc`), `observe` 21, `data` 26, `app` 15 |
| `internal/service` | 103 | — | `crypto` 13 (in eight role directories: `aead`, `agree`, `hash`, `kdf`, `key`, `mac`, `password`, `sign`), `security` 5, `net` 6, `proc` 12, `observe` 26, `data` 26, `app` 15 |
| `pkg/v1` | 87 | `errs`, `clock` | `concur` 6, `collections` 2, `crypto` 7 (the AEAD at the family's root and six scheme facades), `security` 5, `net` 6, `proc` 10 (the capability preflight at the family's root and nine facades), `observe` 6, `data` 28, `app` 15 |

`pkg/v1` has eleven top-level directories where it had 55. `internal/core`
and `internal/service` hold the same seven families and no package outside
them — the core's `crypto`, `net` and `proc` are their families' root
packages; the kernel and `pkg/v1` add the families only they need. Where the code
settled what the record left open, or departed from it:

- **The logger keeps its name.** It is `observe/logger` in every layer, not
  §2's `observe/log`: a Go package is named after its directory, so the move
  would have renamed the package every importer spells `logger.` and set it
  against the standard library's `log` in every file that needs both.
- **`semver` is `pkg/v1/data/semver`**, in the `data` family rather than at
  the root §1 gave it (ADR 0159, as implemented): the root of `pkg/v1` holds
  `errs` and `clock` alone.
- **The kernel took three families**, where §2 expected only `concur` and
  `collections` (ADR 0159, as implemented): `buffer` joined `concur`, `cache`
  joined `collections`, and `fs` holds the filesystem measurements.
- **A child is a sibling when it never needed its parent.** `sse`,
  `websocket` and `static` were children of `server` in the old tree and
  imported nothing of it; they are siblings under `pkg/v1/net`, as their
  engines already were under `internal/service/net`. `sdnotify` and
  `sdlisten` are `proc/systemd/notify` and `proc/systemd/listen` in both
  layers that hold them, and their packages are named `notify` and `listen`.
- **A domain's parts nest beneath it.** The core's writer registry is
  `observe/logger/writer`, beside `observe/logger/level`, and the engines'
  writers, sinks and middlewares sit under `internal/service/observe/logger`.
  The OpenTelemetry model `metrics` and `trace` share is
  `internal/core/observe/otel`; the helpers two engines share sit where Go's
  `internal/` rule admits exactly those engines —
  `internal/service/observe/internal/otlp`,
  `internal/service/observe/logger/internal/logfile`,
  `internal/service/proc/internal/rlim`.
- **Every codec is in one tree** (§3): the sixteen formats, `strictjson` (its
  request-body form in `strictjson/httpbody`), `jsonshape` and `jsonpatch`
  under `data/codec` in the service and the facades, and `data/transform` a
  sibling with a facade of its own.
- **One family per commit, not per pull request** (§5): each family moved in
  one rename-only commit on the series' integration branch — `git mv`, the
  path substitution, regenerated `BUILD.bazel` files and READMEs, and the
  hand-kept tables — and the families reach `main` together, in the series'
  one pull request, which names the paths it moves and is sized by the
  `release:minor` label a maintainer sets (ADR 0135).
- **The measurement of §Context, re-run on the final tree**: `go list -deps`
  of `pkg/v1/data/codec/strictjson` holds neither `pkg/v1/data/codec` nor any
  codec, that of `pkg/v1/net/websocket` holds no `pkg/v1/net/server`, and no
  child of `pkg/v1/crypto` or of `pkg/v1/proc` links its family's root.
- **§6 as built**: `check-domain-docs.sh` reads the root `CLAUDE.md`'s one
  `core/` block — a family's packages may be written `family/{a, b}` — and
  compares it with every directory under `internal/core` that holds Go code,
  at any depth.

## Alternatives considered

- **Group `pkg/v1` only and leave `internal/` flat.** Rejected: the public
  tree and the tree that implements it would disagree, and every reader would
  map one onto the other in their head.
- **Group `internal/` only and freeze `pkg/v1`.** Breaks no consumer, and
  leaves flat the one tree consumers actually read.
- **Alias packages at the old paths for N releases.** Rejected: twice the
  surface, a deprecation schedule to maintain, for a v0 module whose external
  consumers are unknown.
- **A depth limit of two.** Rejected: `crypto/sign/ed25519sig` is three, and a
  fixed limit either forbids a logical directory or allows an illogical one —
  the test is whether a directory names something its members share.
- **Keep "siblings, not children" for the crypto facades.** Rejected: its only
  reason is false, as measured above.

## Deferred

- A mechanical check that the three layers use the same family names. ADR 0160
  checks that the service domains and the core domains are the same list; the
  family directories above them are checked by review until then.

## References

- `go list -deps ./pkg/v1/codec/strictjson`, `go list -deps ./pkg/v1/server/websocket`
  — the measurement that a child does not link its parent, on the tree this
  record was written against; on the final tree,
  `go list -deps ./v1/data/codec/strictjson` and
  `go list -deps ./v1/net/websocket`, run from `pkg/`.
- [Go modules reference — v0 major version](https://go.dev/ref/mod#v0-major).
