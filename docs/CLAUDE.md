<!-- updated: 2026-05-18T14:30:00Z -->
# docs/

## Purpose

Long-form SDK documentation. ADRs here are the source of truth for cross-cutting decisions; package-level READMEs are the short-form mirror.

## Contents

| File | Topic | Status |
|---|---|---|
| `adr/0001-sdk-go-multimodule-layout.md` | 4-layer architecture, 5 Go modules glued by `go.work` | Accepted |
| `adr/0002-sdk-errors-package.md` | Layered typed `errs` package, Public/Private split, registry, breaking changes | Accepted (§Registry superseded by ADR 0005) |
| `adr/0003-sdk-codec-package.md` | Universal codec surface (10 formats + baseenc) behind `Marshal/Unmarshal` dispatch | Accepted (M1+M2+M3+M4 shipped) |
| `adr/0004-sdk-bazel-build-system.md` | Bazel 9 as single build/test system; visibility replaces depguard | Accepted |
| `adr/0005-sdk-error-codes-dotted-quad.md` | `Code uint32` laid out `MM.LL.PP.SS`, wrap trail, CIDR-style `PrefixMatcher` | Accepted (supersedes ADR 0002 §Registry) |
| `adr/0006-sdk-error-code-registry-extension.md` | Registry assignments for logger v2 sinks/middleware + `kernel/ring` | Accepted (amends ADR 0005 §Registry) |
| `adr/0007-sdk-release-and-versioning.md` | Impact-driven patch tags `pkg/<major>/vX.Y.Z`, docs versioning via tag snapshots | Accepted |
| `adr/0008-readme-from-code-generation.md` | `README.md` generated from Go doc comments via gomarkdoc | Accepted |
| `adr/0009-pkg-public-module-resolvability.md` | Published `pkg/<major>` must be `go get`-resolvable + accessible (no local `replace`) | Accepted (requirement); impl Deferred |
| `adr/0010-kernel-recycler-primitive.md` | Generic kernel `Pool[T]` + `CappedPool[T]`; `buffer` + `core/codec/scratch` become consumers | Accepted |
| `adr/0011-kernel-snapshot-primitive.md` | Generic kernel `Value[T]` copy-on-write container; codec registry consolidates onto it | Accepted |
| `adr/0012-logger-writer-registry.md` | Named, config-driven `Sink` factory registry (`writer`); console/file in-tree + AWS s3/cloudwatch under third-party/ (root module) | Accepted (amends ADR 0005/0006 §Registry) |
| `adr/0013-sdk-crypto-domain.md` | Crypto domain — `AEAD` `Seal`/`Open` with hidden nonce; stdlib AES-256-GCM default, x/crypto schemes under third-party/ | Accepted (amends core purpose + ADR 0005 §Registry) |
| `adr/0014-sdk-transform-crypto-ports-config-topology.md` | The verb wave — `core/transform` (5th sibling), crypto MAC/Agreement/StreamSealer ports + KeyEnvelope/KeyTree, `logger.FromConfig` + `ConfigDecoder` | Accepted (amends core purpose + ADR 0012/0013 + ADR 0005 §Registry) |
| `adr/0015-sdk-logger-writer-taxonomy-and-rotation.md` | Writer taxonomy + `depTier` as a first-class property; default writers console+file; rotation off-by-default → compress + 24h archive + 7-day retention; dep-light DB/transport gate (WI-9 dbsink shipped; WI-10/WI-11 gated) | Accepted (amends `core/writer` purpose + ADR 0005 §Registry) |
| `adr/0016-sdk-process-supervision-domain.md` | OS process-supervision domain (`proc`): 6th core sibling; `process`/`signal`/`reaper`/`rlimit`/`cgroup`/`sdnotify` facades; central error block `0.2.6.*`; build-tag platform selection | Accepted |
| `adr/0017-pkg-bare-module-path.md` | Public module is the bare `…/pkg` (Go forbids the `/v1` suffix); code stays under `v1/`, imports unchanged; tag shape `pkg/vX.Y.Z`, first release `v0.1.0` alpha | Accepted (amends ADR 0009 path, ADR 0007 tag shape, ADR 0001 module path) |
| `adr/0018-sdk-cross-platform-portability.md` | Cross-platform portability strategy — two bars (build + runtime); uniform `UnsupportedPlatform` contract; `_linux`/`_unix`/`_bsd`/`_windows`/`_other` split; `bazel-ci.yml` cross-build job (build) + `e2e-vm.yml` (runtime, real kernels) gates; OpenBSD `RLIMIT_AS` precedent; native-backend roadmap (FreeBSD `rctl`, Windows Job Objects, BSD `procctl`) | Accepted |

## ADR conventions

- Sequential numbering (`0001`, `0002`, …). Never reuse a number.
- Filename slug = short kebab-case summary of the decision.
- Header fields: `Status`, `Date`, `Deciders`, optional `Supersedes`, `Superseded by`, `Amends`, `Related`.
- Standard sections: Context, Decision, Consequences / Semantics, Breaking changes, Why not …, Deferred, References.
- Immutable after merge. Supersede via a new ADR that references the old one (e.g. ADR 0005 supersedes ADR 0002 §Registry; ADR 0006 amends ADR 0005 §Registry).

## Registry coupling

The dotted-quad allocation table in `adr/0005-…` + the extension in `adr/0006-…` is the **documentary source of truth**; the AST audit test in `internal/kernel/errs/registry_external_test.go` embeds the same table as the **executable source of truth**. Keep the two in sync manually on every change — the audit catches drift on uniqueness and `reason = screamingSnake(varName)`.

## Do NOT

- Put feature documentation here. Packages document themselves via `README.md` next to their code.
- Delete an accepted ADR. Mark it superseded by adding a new ADR and updating both `Status` lines.
- Re-number existing ADRs, even after a supersede chain.
- Edit an ADR's Decision section after merge — write a new ADR.

## Subtree

- `adr/` — Architecture Decision Records (eighteen accepted to date — see table above)
