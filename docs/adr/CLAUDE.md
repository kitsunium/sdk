<!-- updated: 2026-05-18T14:30:00Z -->
# docs/adr/

## Purpose

Architecture Decision Records. Each ADR captures one cross-cutting decision (layout, error model, build system, code allocation) with rationale, alternatives considered, and impact. ADRs are authoritative — when an ADR and a `CLAUDE.md` disagree, the ADR wins and the CLAUDE.md gets corrected. Treat ADRs as append-only: never edit a merged ADR's decision; if a follow-up changes direction, write a new ADR that supersedes the previous one and update its `Status` field.

## Contents

| File | Decision | Status |
|---|---|---|
| `0001-sdk-go-multimodule-layout.md` | Per-sublayer Go modules (`internal/kernel`, `internal/core`, `internal/service`, `pkg/v1`) with `replace` directives + `go.work` umbrella | Accepted |
| `0002-sdk-errors-package.md` | Typed `errs.Define` / `errs.Wrap` infrastructure with `Public` / `Private` split | Accepted (Registry section superseded by 0005) |
| `0003-sdk-codec-package.md` | Universal `Codec` / `StreamingCodec` / `Appender` triad + 10-format registry | Accepted |
| `0004-sdk-bazel-build-system.md` | Bazel 9 with `rules_go` + `gazelle` + `package_group`/`visibility` for layer firewall | Accepted |
| `0005-sdk-error-codes-dotted-quad.md` | `Code uint32` packed as `MM.LL.PP.SS` with wrap trail + canonical `Error()` regex | Accepted |
| `0006-sdk-error-code-registry-extension.md` | Logger v2 + `ring` code-range allocations on top of 0005 | Accepted |
| `0007-sdk-release-and-versioning.md` | Impact-driven patch tags `pkg/<major>/vX.Y.Z` + docs versioning from tag snapshots | Accepted |
| `0008-readme-from-code-generation.md` | `README.md` generated from Go doc comments (gomarkdoc) | Accepted |
| `0009-pkg-public-module-resolvability.md` | Published `pkg/<major>` must be `go get`-resolvable + accessible; release tags the whole module chain | Accepted; mechanism implemented |
| `0010-kernel-recycler-primitive.md` | Generic `recycler.Pool[T]` + `CappedPool[T]` object pools; `buffer`/`scratch` become consumers | Accepted |
| `0011-kernel-snapshot-primitive.md` | Generic `snapshot.Value[T]` copy-on-write container; codec registry consolidates onto it | Accepted |
| `0012-logger-writer-registry.md` | Named, config-driven `Sink` factory registry (`writer`); console/file in-tree + AWS s3/cloudwatch under third-party/ (root module) | Accepted (amends 0005/0006 §Registry) |
| `0013-sdk-crypto-domain.md` | Crypto domain — `AEAD` `Seal`/`Open` with hidden nonce; stdlib AES-256-GCM default, x/crypto schemes under third-party/ | Accepted (amends core purpose + 0005 §Registry) |
| `0014-sdk-transform-crypto-ports-config-topology.md` | The verb wave — `core/transform` (5th sibling), crypto MAC/Agreement/StreamSealer ports + KeyEnvelope/KeyTree, `logger.FromConfig` + `ConfigDecoder`, single-MR sequencing | Accepted (amends core purpose + 0012/0013 + 0005 §Registry) |
| `0015-sdk-logger-writer-taxonomy-and-rotation.md` | Writer taxonomy + `depTier` first-class property; default console+file (`DefaultMulti`); rotation off-by-default → compress + 24h archive + 7-day retention; dep-light DB/transport gate (WI-9 `dbsink` shell shipped; WI-10/WI-11 vendored writers gated) | Accepted (amends `core/writer` purpose + 0005 §Registry) |
| `0016-sdk-process-supervision-domain.md` | OS process-supervision domain (`proc`): 6th core sibling; `process`/`signal`/`reaper`/`rlimit`/`cgroup`/`sdnotify` facades; central error block `0.2.6.*`; build-tag platform selection | Accepted |
| `0017-pkg-bare-module-path.md` | Public module is the bare `…/pkg` (Go forbids the `/v1` suffix); code stays under `v1/`, imports unchanged; tag `pkg/vX.Y.Z`, first release `v0.1.0` alpha | Accepted (amends 0009 path / 0007 tag shape / 0001 module path) |
| `0018-sdk-cross-platform-portability.md` | Cross-platform portability strategy — build bar + runtime bar; uniform `UnsupportedPlatform` contract; `_linux`/`_unix`/`_bsd`/`_windows`/`_other` split convention; `bazel-ci.yml` cross-build + `e2e-vm.yml` gates; OpenBSD `RLIMIT_AS` precedent; native-backend roadmap (FreeBSD `rctl`, Windows Job Objects, BSD `procctl`) | Accepted |
| `0019-pkg-errs-public-construction.md` | Public error construction API (`pkg/v1/errs.New`/`Wrap`/`Field` via non-panicking `kernel/errs.NewRuntime`) + third-party Major reservation `0x40–0x7F` (`MinAppMajor`/`MaxMajor`) | Accepted (amends 0002 / 0005 §Registry) |
| `0020-errs-audit-dual-reason-derivation.md` | errs AST audit accepts `screamingSnake(varName)` OR `screamingSnake(CodeConst − "Code")`; closes the `//:audit_sources` gap excluding ~10 namespaced emitters | Accepted (amends 0005 §Semantics / formalises 0006) |
| `0021-sdk-codec-bson.md` | BSON codec (M5) in `internal/service/codec/bson` over `mongo-driver/bson` (library-backed precedent); non-streaming + Appender; block `0.3.36.*` | Accepted (extends 0003 §M5) |
| `0022-sdk-codec-hcl.md` | HCL codec (M5) quarantined in `third-party/codec/hcl` (`hcl/v2` would introduce the banned `x/sys` into service — see 0034, which corrects the original "downgrade" wording); opt-in, not in `pkg/v1/codec`; block `0.3.37.*` | Accepted (extends 0003 §M5; follows 0012, contrasts 0021; mechanism corrected by 0034) |
| `0023-sdk-schema-codecs.md` | Schema codecs (M6) opt-in under `third-party/codec/` (schema-bound, break universal round-trip); Protobuf concrete `0.3.38.*`; Avro/Cap'n Proto deferred | Accepted (extends 0003 §M6) |
| `0024-sdk-id-domain.md` | Identifier-generation domain (`id`): 7th core sibling, `Generator`/`Scheme` registry (UUIDv4/v7, ULID, snowflake), canonical-string output, stdlib-only/cross-OS; opens Phase-B; blocks `0.2.7.*`/`0.3.39.*`. Since extended in place with NanoID, KSUID and TypeID (same block; TypeID constructor-only per ADR 0031) | Accepted (amends core purpose; opens Phase B) |
| `0025-sdk-cache-kernel.md` | Generic LRU+TTL `Cache[K,V]` as a **kernel** primitive (domain-neutral, stdlib-only, reuses `clock`); `Fetch` not `Get`; no error codes; `pkg/v1/cache` aliases | Accepted (kernel primitive, Phase B) |
| `0026-sdk-resilience-domain.md` | Reliability domain (`resilience`): 8th core sibling, **no registry** (concrete `Runner` policies: retry/breaker/ratelimit/bulkhead/timeout); composable; block `0.2.8.*` | Accepted (Phase B; proc-style no-registry sibling) |
| `0027-sdk-metrics-domain.md` | Observability domain (`metrics`): 9th core sibling, instruments + `Meter` + exporter registry (writer-registry model); in-mem meter + text exporter; block `0.2.9.*`; labels/Prometheus/OTLP deferred | Accepted (Phase B) |
| `0028-sdk-config-domain.md` | Configuration domain (`config`): 10th core sibling, `Source`/`Validator`/`Watcher` ports; env+file loader + cross-OS **poll** watcher; closes the Phase-B wave; block `0.2.10.*` | Accepted (Phase B; closes the wave) |
| `0029-sdk-net-domain.md` | Network domain (`net`) as the 11th core sibling: one contract package, service engines for TLS identity / guarded client / listener engine, `pkg/v1` facades; goroutine-per-connection on the netpoller, `net/http` adapted rather than reimplemented; block `0.2.11.*` | Accepted |
| `0030-stdout-is-a-protocol-channel.md` | No SDK default writes to `os.Stdout`: the registered `text` metrics exporter targets stderr, `ConsoleStderr` becomes the `ConsoleStream` zero value (and the absent/empty `target` maps to it); stdout stays reachable by naming it | Accepted (amends 0027 §Decision 2 / 0015 §D3) |
| `0033-consumer-rule-enforcement.md` | SDK rules are enforced on consumers at BUILD time by `tools/sdkguard`, a stdlib-only CLI. Runtime detection was measured impossible (`log/slog` is not a module; a local `slog.Logger` never touches `slog.Default`), and a vettool needs `x/tools` — which `tools/*` cannot take without breaking its Bazel build. Rules target constructs not imports, and split invariant/convention so adoption is incremental. Also warns (never fails, by default) when the consumer's go.mod pins an SDK older than the latest release — ADR 0007 cuts patches for internal fixes invisible in a public-API changelog | Accepted |
| `0032-logger-slog-bridge.md` | `log/slog` is permitted in exactly one package (`pkg/v1/logger/slogbridge`), as an adapter to a foreign ecosystem at the public edge; the domain keeps its "never log/slog" rule. Removes the two-parallel-loggers pattern and the three silent defects it produces | Accepted (qualifies the rule in `core/logger/level`) |
| `0034-hcl-quarantine-rationale-corrected.md` | The HCL quarantine stands; its stated mechanism does not. MVS selects the **maximum** required version, so a dependency asking for a lower one cannot downgrade a higher requirement. Measured: adding `hcl/v2` to a copy of `internal/service` **introduces** `x/sys` at `v0.20.0` (via `x/tools`) into a module that bans it. A ban violation, not a version regression | Accepted (amends 0022 §Context.1 / §Why not — mechanism only) |
| `0035-pp-range-ownership-enforcement.md` | `PP`-range ownership gets the enforcement 0005 §Registry assumed: exclusivity (one package per `MM.LL.PP`) + conformance to a hand-maintained `codeRangeOwners` table, keyed on Code **declarations** rather than `Define` calls. The table is never generated from the constants — a derived table would record a squatter as the owner. Four fixtures prove the checks fail on violations | Accepted (supplies enforcement for 0005 §Registry) |
| `0031-policy-zero-values-are-never-inert.md` | A policy constructor never returns an inert policy: **clamp** where a working default needs no explanation (`BreakerConfig.OpenDuration` → 30s), **refuse** where any SDK-chosen value would be arbitrary (`RateLimiterConfig.Rate`, `NewTimeout`) via the new `PolicyMisconfigured` sentinel `0.2.8.6` | Accepted (amends 0026 §Decision 2) |

## Conventions

- File name: `NNNN-<short-slug>.md` where `NNNN` is the next zero-padded number.
- **Two branches writing ADRs at once: claim distinct numbers, then merge in
  numeric order.** The number is claimed when the file is committed on a branch,
  not when the branch merges, so a second branch reads the first one's claim and
  takes the number after it. Merging low-to-high keeps `main` contiguous at every
  point in time, which is what the check below verifies. Merging high-first
  leaves `main` with a real gap until the other branch lands — legal in the end,
  but the check fails in between, so do not do it. **Renumbering to close that
  window is the one wrong answer**: the branch that already holds the lower
  number would then collide with it, and a number is cited from ADR headers,
  package docs and source comments long before it merges.
- Header block: **Status**, **Date**, **Deciders**, plus **Supersedes** / **Superseded by** / **Amends** / **Related** where they apply.
- Standard sections, in this order: **Context**, **Decision**, **Consequences / Semantics**, **Breaking changes**, **Alternatives considered** and/or **Why not <option>**, **Deferred**, **References**. This is the same list as `docs/CLAUDE.md` §ADR conventions — keep the two in step; when they disagree, the two are both wrong until they agree.
- A standard section with nothing to say says so rather than being dropped — `Breaking changes` → "None. `<domain>` is a new domain in this change set" is the house form (ADR 0026/0027/0028). A missing heading reads as an oversight; an explicit "None" reads as a decision.
- Mermaid / ASCII diagrams welcome where they reduce reading time.
- Cross-link with relative paths: `../adr/0005-…md` so links survive a move.
- Use `1.2.0.3` style for codes in prose, never the hex literal.

## Do NOT

- Edit a merged ADR's `Decision` section. Supersede it via a new ADR that links to the old one and flips the old `Status` to `Superseded by NNNN`.
- Store implementation detail here — keep ADRs about the *decision* and the *constraint*, not the code. Implementation specifics live in the package `CLAUDE.md` / `README.md`.
- Reference internal commit SHAs or PR numbers in the body — the URL in a markdown link is the only authoritative reference.

## Verification

```
ls docs/adr/ | grep -E '^[0-9]{4}-' | sort
# every ADR must follow the NNNN-slug.md pattern; no gaps in the numeric sequence.

# On a branch, a gap is expected when a lower number is claimed on another
# branch — check before blaming the sequence, and merge low-to-high.
# The fetch is not optional: git ls-remote reads the remote without updating
# origin/*, so without it git ls-tree silently sees nothing for a branch this
# clone has never fetched, and the check reports "no one holds it".
git fetch --prune origin
git for-each-ref --format='%(refname)' refs/remotes/origin \
  | grep -v '^refs/remotes/origin/HEAD$' \
  | while read -r ref; do
      git ls-tree --name-only "$ref" docs/adr/ \
        | grep -oE '[0-9]{4}' | sed "s#^#${ref#refs/remotes/origin/} #"
    done | sort -u -k2
```
