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
| `adr/0019-pkg-errs-public-construction.md` | Public error construction API (`pkg/v1/errs.New`/`Wrap`/`Field` via non-panicking `kernel/errs.NewRuntime`) + third-party Major reservation `0x40–0x7F` (`MinAppMajor`/`MaxMajor`) | Accepted (amends ADR 0002 / 0005 §Registry) |
| `adr/0020-errs-audit-dual-reason-derivation.md` | errs AST audit accepts `screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")`; closes the `//:audit_sources` gap excluding ~10 namespaced emitters (ring + logger middleware/sinks) | Accepted (amends ADR 0005 §Semantics / formalises ADR 0006) |
| `adr/0021-sdk-codec-bson.md` | BSON codec (M5) over `go.mongodb.org/mongo-driver/bson` in `internal/service/codec/bson` (library-backed codec precedent, not third-party quarantine); non-streaming + Appender; error block `0.3.36.*` | Accepted (extends ADR 0003 §M5) |
| `adr/0022-sdk-codec-hcl.md` | HCL codec (M5) **quarantined** in `third-party/codec/hcl` (root module) because `hcl/v2` would **introduce** the banned `x/sys` into `internal/service` (ADR 0034 corrects the original "downgrade" wording); opt-in, NOT in `pkg/v1/codec`; first `third-party/codec/` subtree; error block `0.3.37.*` | Accepted (extends ADR 0003 §M5; follows ADR 0012 quarantine, contrasts ADR 0021) |
| `adr/0023-sdk-schema-codecs.md` | Schema codecs (M6): opt-in, under `third-party/codec/` — schema-bound (can't honour the universal round-trip contract); Protobuf concrete (`0.3.38.*`); Avro/Cap'n Proto deferred | Accepted (extends ADR 0003 §M6) |
| `adr/0024-sdk-id-domain.md` | Identifier-generation domain (`id`): 7th core sibling, `Generator`/`Scheme` registry (UUIDv4/v7, ULID, snowflake); stdlib-only, cross-OS; opens the Phase-B new-domain wave; error block `0.2.7.*`/`0.3.39.*`. Since extended in place with NanoID, KSUID and TypeID (same block; TypeID constructor-only per ADR 0031) | Accepted (amends `internal/core` purpose) |
| `adr/0025-sdk-cache-kernel.md` | Generic LRU+TTL `Cache[K,V]` as a **kernel** primitive (domain-neutral, stdlib-only, reuses `clock` for testable TTL); `Fetch` not `Get`; no error codes; `pkg/v1/cache` type aliases | Accepted (kernel primitive, Phase B) |
| `adr/0026-sdk-resilience-domain.md` | Reliability domain (`resilience`): 8th core sibling (no registry), composable `Runner` policies — retry/circuit-breaker/rate-limit/bulkhead/timeout; error block `0.2.8.*` | Accepted (Phase B) |
| `adr/0027-sdk-metrics-domain.md` | Observability domain (`metrics`): 9th core sibling, Counter/Gauge/Histogram + `Meter` + `Exporter` registry; in-memory meter + stdlib text exporter; error block `0.2.9.*`; labels + Prometheus/OTLP deferred | Accepted (Phase B) |
| `adr/0028-sdk-config-domain.md` | Configuration domain (`config`): 10th core sibling, `Source`/`Validator`/`Watcher` ports; env+file layering, JSON round-trip decode, cross-OS poll watcher; closes Phase B; error block `0.2.10.*` | Accepted (Phase B) |
| `adr/0030-stdout-is-a-protocol-channel.md` | No SDK default writes to `os.Stdout`: the registered `text` metrics exporter targets stderr, `ConsoleStderr` becomes the `ConsoleStream` zero value (and an absent or empty `target` maps to it); stdout stays reachable by naming it | Accepted (amends ADR 0027 §Decision 2 / ADR 0015 §D3) |
| `adr/0031-policy-zero-values-are-never-inert.md` | A policy constructor never returns an inert policy: **clamp** where a working default needs no explanation (`BreakerConfig.OpenDuration` → 30s), **refuse** where any SDK-chosen value would be arbitrary (`RateLimiterConfig.Rate`, `NewTimeout`) via the `PolicyMisconfigured` sentinel `0.2.8.6` | Accepted (amends ADR 0026 §Decision 2) |
| `adr/0034-hcl-quarantine-rationale-corrected.md` | The HCL quarantine stands; its stated mechanism does not. Under MVS a dependency requiring a lower version cannot downgrade a higher requirement; measured, adding `hcl/v2` to `internal/service` **introduces** `x/sys` (via `x/tools`) into a module that bans it | Accepted (amends ADR 0022 §Context.1 — mechanism only) |
| `adr/0035-pp-range-ownership-enforcement.md` | `PP`-range ownership enforced by exclusivity + a hand-maintained `codeRangeOwners` table, keyed on Code declarations rather than `Define` calls; the table is never generated from the constants it audits | Accepted (supplies enforcement for ADR 0005 §Registry) |
| `adr/0036-sdk-codec-form-urlencoded.md` | form-urlencoded: repetition is the only array syntax; framework dialects and last-wins refused; non-bijectivity stated rather than faked | Accepted (extends ADR 0003) |
| `adr/0037-sdk-codec-multipart.md` | multipart/form-data: the boundary is not in the body, so the codec grows extension interfaces (`BoundaryCodec`, `BoundaryProvider`) instead of widening the universal contract | Accepted (extends ADR 0003) |
| `adr/0038-id-schemes-and-the-unregistered-typeid.md` | NanoID, KSUID, TypeID — and a `Scheme` deliberately absent from the registry, which makes the registry non-exhaustive by design | Accepted (extends ADR 0024) |

## ADR conventions

- Sequential numbering (`0001`, `0002`, …). Never reuse a number.
- Filename slug = short kebab-case summary of the decision.
- Header fields: `Status`, `Date`, `Deciders`, optional `Supersedes`, `Superseded by`, `Amends`, `Related`.
- Standard sections: Context, Decision, Consequences / Semantics, Breaking changes, Why not …, Deferred, References.
- Immutable after merge. Supersede via a new ADR that references the old one (e.g. ADR 0005 supersedes ADR 0002 §Registry; ADR 0006 amends ADR 0005 §Registry).

## Registry coupling

The dotted-quad allocation table in `adr/0005-…` + the extension in `adr/0006-…` is the **documentary source of truth**; the AST audit test in `internal/kernel/errs/registry_external_test.go` embeds the same table as the **executable source of truth**. Keep the two in sync manually on every change — the audit catches drift on uniqueness and `reason = screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")` (ADR 0006/0020) over every package in `//:audit_sources`.

`docs/error-codes.yaml` is the **generated human-readable mirror** of every `errs.Code` constant in the tree (one entry per code: dotted-quad, const name, hex, package). Regenerate with `make error-codes` (`scripts/gen-error-codes.sh`); the `scripts/pre-commit/check-error-codes-drift.sh` guard fails the commit when it is stale. It is a convenience index, not authoritative — the AST audit remains the executable gate.

## Do NOT

- Put feature documentation here. Packages document themselves via `README.md` next to their code.
- Delete an accepted ADR. Mark it superseded by adding a new ADR and updating both `Status` lines.
- Re-number existing ADRs, even after a supersede chain.
- Edit an ADR's Decision section after merge — write a new ADR.

## Subtree

- `adr/` — Architecture Decision Records (see the table above; it is the count, so no separate number goes stale here)
