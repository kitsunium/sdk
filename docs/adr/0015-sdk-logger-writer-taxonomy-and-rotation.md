# ADR 0015 — Writer taxonomy, depTier as a first-class writer property, default rotation policy, and a dep-light DB/transport gate

- **Status**: Accepted
- **Date**: 2026-06-02
- **Deciders**: SDK maintainers
- **Related**: ADR 0001 (multi-module layout), ADR 0005 (dotted-quad codes), ADR 0006 (registry extension), ADR 0012 (writer registry — the pattern this ADR taxonomises), ADR 0013 (crypto domain — the `third-party/*` quarantine precedent), ADR 0014 (the verb wave — `rotfile`, `transform`, `logger.FromConfig` + the `Decoder` config hook this ADR builds the YAML topology on)
- **Amends**:
  - `internal/core/writer/CLAUDE.md` — promotes **`depTier`** (stdlib / vendor-in-root / third-party) from an implicit folder convention to a **documented, first-class property of every writer**, and states the **never-in-tree** placement rule for vendor-backed writers;
  - the ADR 0005 §Registry allocation table — reserves the service octets `0x1B.4` (rotfile decode), `0x1E` (net transport), `0x1F` (journald) and the `third-party/db/writer/*` octets `0x20` (mysql), `0x21` (clickhouse), `0x22` (redis-stream); each is **declared here, allocated, ADR-gated where vendored, and minted only in its own package's `codes.go`** when the corresponding WI lands.
- **Gates**: WI-9 (`internal/service/writer/dbsink` batching shell), WI-10 (`third-party/db/writer/{mysql,clickhouse}`), WI-11 (`third-party/db/writer/redis` stream transport). Those items MUST NOT begin until this ADR is Accepted — they introduce vendor SDKs and a new core seam, which the SDK's one-decision-per-ADR rule requires be frozen *before* code.

## Context

ADR 0012 gave the logger a **named, config-driven writer registry**: a `Name`
resolves to a `Factory` that yields a `core/logger.Sink`. ADR 0014 added the
`rotfile` sink, the `transform` compressors, `logger.FromConfig`, and the
`Decoder` factory-extension hook that lets a YAML option map be translated into
a factory's typed `Config`. What ADR 0012/0014 left implicit is the **shape of
the writer space as it grows**: which writers may live in-tree, which must be
quarantined, and how a consumer reasons about the dependency cost of turning a
writer on.

The user goal driving this wave is a *"perfect"* logger: zero-alloc on the
producer hot path, **multi-writer by default** (console **and** file active out
of the box), and a broad, configurable writer taxonomy — console / file / DB
(SQL + document store) / API (S3 / CloudWatch) / network transport / journald —
all CPU-minimal and RAM-minimal. Several of these writers cannot be honest
without pulling a heavy vendor SDK (a MySQL driver, a ClickHouse client, a Redis
client). The SDK's **dep-light invariant** is non-negotiable: `pkg/v1/*` and its
blank-import activators stay stdlib-only; `go list -deps ./pkg/v1/...` must show
**zero** `x/crypto` and **zero** cloud/DB vendor SDKs (ADR 0013 established the
`third-party/*` quarantine; the AWS writers under `third-party/aws/writer/*`
are the working precedent).

Three things are therefore undecided and block the next writers:

1. **There is no documented taxonomy.** New contributors have no rule that tells
   them whether a `clickhouse` writer goes in `internal/service/writer/` or in
   `third-party/db/writer/`. The placement has so far been folklore ("AWS went
   to third-party, so…").
2. **`depTier` is not a first-class concept.** The dependency cost of a writer —
   *stdlib only* vs *vendored in the root module* vs *quarantined behind an
   opt-in third-party blank import* — is the single most important property for a
   dep-light consumer, yet it is nowhere named. It is implied by the folder a
   writer's package happens to sit in.
3. **The default rotation policy is unstated.** `rotfile` (ADR 0014) ships
   size-rotation (`MaxBytes`), count retention (`MaxBackups`), age retention
   (`MaxAgeDays`), and gzip `Compress`, but the SDK has never written down the
   *defaults* a consumer gets, nor the **YAML-reachability** of those knobs, nor
   the interval-triggered ("every 24h") rotation that the user goal asks for.

## Decision

### D1 — The writer taxonomy (classes are documentation, not code)

A *writer* is a `core/writer.Factory` (ADR 0012) that yields a
`core/logger.Sink`. Writers are grouped into **classes** purely for
documentation and discoverability — there is **no class enum, no class field,
and no per-class interface**. The registry stays a flat `Name → Factory` map; a
class is a label in this ADR and in `core/writer/CLAUDE.md`, never a runtime
type. The frozen-at-this-ADR class list:

| Class | Examples (Name) | Placement | depTier |
|---|---|---|---|
| **console** | `"console"` | `internal/service/writer/console` | `stdlib` |
| **file** | `"file"`, `"rotfile"` | `internal/service/writer/{file,rotfile}` | `stdlib` |
| **transport** | `"net"` (TCP/UDP/Unix), `"journald"` | `internal/service/writer/{nettransport,journald}` | `stdlib` |
| **api** | `"s3"`, `"cloudwatch"` | `third-party/aws/writer/{s3,cloudwatch}` | `third-party` |
| **db** | `"mysql"`, `"clickhouse"`, `"redis-stream"` | `third-party/db/writer/{mysql,clickhouse,redis}` | `third-party` |

The list is **open** — a future ADR may add a class (e.g. a `mq` class for a
Kafka/NATS writer) — but adding a class is an ADR-level act, exactly as growing a
core sibling is (ADR 0014 §D1). The placement column is **mechanical**, governed
by D2.

### D2 — `depTier`: a first-class, three-valued writer property

Every writer has exactly one **`depTier`**, the property that tells a consumer
what turning the writer on costs the dependency graph:

| `depTier` | Meaning | Where the writer's package lives | Activation |
|---|---|---|---|
| `stdlib` | the writer's package imports **only** the Go standard library (+ kernel/core/service) | `internal/service/writer/*` (in-tree) | blank-import of the in-tree package, wired by `pkg/v1/logger` |
| `vendor-root` | the writer needs a vendor dep that already lives in the **root** `go.mod` and nothing requires the root module | `third-party/*` (root module) | opt-in blank import of the `third-party/*` package |
| `third-party` | the writer needs a vendor SDK that must be quarantined so it never reaches `pkg/v1` | `third-party/*` (root module or its own module) | opt-in blank import of the `third-party/*` package |

**The mechanical placement rule (never-in-tree):**

> A writer whose `depTier` is anything other than `stdlib` MUST NOT have its
> factory package under `internal/service/writer/*`. It lives under
> `third-party/*` and is reached only by an explicit, opt-in blank import. The
> `internal/service/writer/*` tree is **stdlib-only by construction**, so that
> `go list -deps ./pkg/v1/...` is provably free of vendor SDKs without auditing
> every file.

`depTier` is **documented, not encoded**: it is a column in
`core/writer/CLAUDE.md`'s writer table and a sentence in each writer package's
`CLAUDE.md`, not a Go field. Encoding it as runtime data would force the registry
to know about vendor tiers, which would re-couple core to the very deps the tier
exists to quarantine. The **executable** enforcement of `depTier == stdlib` for
in-tree writers is the existing dep-light check (`go list -deps ./pkg/v1/...`
shows zero vendor SDKs) plus Bazel visibility — not a new test.

### D3 — Default-active writers and the default rotation policy

- **Default-active writers are `console` AND `file`.** A logger built with no
  explicit topology fans out to both; this is the *multi-writer by default*
  contract. The mechanism is `pkg/v1/logger`'s topology builder (ADR 0014), not
  a change to any core interface.
- **File rotation is OFF by default.** A plain `file` writer never rotates. A
  consumer opts into rotation by selecting the `rotfile` writer (ADR 0014).
- **When rotation is enabled, the default retention policy is:** compress rotated
  files (gzip), archive on a **24h interval** *and* on size, and keep **7 days**
  of backups. Concretely the documented defaults for `rotfile` are
  `Compress = true`, an archive interval of `24h`, and `MaxAgeDays = 7`, every
  one of them **overridable from YAML** through the `rotfile` factory's `Decoder`
  (ADR 0014 §D5). Size rotation (`MaxBytes`) and count retention (`MaxBackups`)
  remain available and YAML-reachable; `MaxBytes <= 0` disables the size trigger,
  leaving the interval trigger as the sole rotation cause.
- **Interval-triggered rotation reuses `CodeRotFileRotateFailed`
  (`0.3.27.2`).** An interval-fired rotation is the *same* rename/compact/reopen
  cycle as a size-fired one; it fails the same way and carries the same dotted-
  quad code. No new error code is minted for the trigger source. The
  interval/time-trigger mechanism itself (a ticker-driven rotation) is **WI-2's
  code item**, not this docs item; this ADR only freezes the *policy and the code
  reuse*.

### D4 — Code allocation (declared here, minted in each package's `codes.go`)

The following dotted-quad codes are **reserved by this ADR** and become real
only when their owning package declares the `const` in its own `codes.go` and the
matching `errs.Define` sentinel in `errors.go`. Codes register **purely by their
`const` declaration + `errs.Define` call** — there is no central table file to
edit. The AST audit (`//internal/kernel/errs:errs_test`) discovers every
`errs.Define` call site automatically and enforces three invariants dynamically:
the `Public` argument is a plain string literal, `reason == screamingSnake(varName)`,
and the **code identifier name is unique** across the tree. (Numeric reuse across
*distinct* identifiers is already tolerated repo-wide — e.g. `CodeWriterNil`
exists at both `0.3.1.1` and `0.3.13.1`; the audit keys on the identifier name,
not the numeric value.)

| Code | Identifier | Package | depTier | Notes |
|---|---|---|---|---|
| `0.3.27.2` | `CodeRotFileRotateFailed` | `internal/service/writer/rotfile` | `stdlib` | **REUSED** for interval rotation — already shipped (ADR 0014), no new alloc |
| `0.3.27.4` | `CodeRotFileDecodeFailed` | `internal/service/writer/rotfile` | `stdlib` | **NEW** — `0.3.27.{1,2,3}` are taken in the `0x1B` block; `.4` is free. Pairs with sentinel `RotFileDecodeFailed`, reason `ROT_FILE_DECODE_FAILED` |
| `0.3.30.1` | `CodeNetTransportDialFailed` | `internal/service/writer/nettransport` | `stdlib` | **NEW** — service octet `0x1E` |
| `0.3.30.2` | `CodeNetTransportWriteFailed` | `internal/service/writer/nettransport` | `stdlib` | **NEW** — ExitCode 74 (EX_IOERR) |
| `0.3.31.1` | `CodeJournaldOpenFailed` | `internal/service/writer/journald` | `stdlib` | **NEW** — service octet `0x1F` |
| `0.3.32.1` | `CodeMySQLClientInitFailed` | `third-party/db/writer/mysql` | `third-party` | **NEW** — service octet `0x20`, ADR-gated |
| `0.3.32.2` | `CodeMySQLInsertFailed` | `third-party/db/writer/mysql` | `third-party` | **NEW** — ExitCode 74, ADR-gated |
| `0.3.33.1` | `CodeClickHouseClientInitFailed` | `third-party/db/writer/clickhouse` | `third-party` | **NEW** — service octet `0x21`, ADR-gated |
| `0.3.33.2` | `CodeClickHouseInsertFailed` | `third-party/db/writer/clickhouse` | `third-party` | **NEW** — ExitCode 74, ADR-gated |
| `0.3.34.1` | `CodeRedisStreamClientInitFailed` | `third-party/db/writer/redis` | `third-party` | **NEW** — service octet `0x22`, ADR-gated |
| `0.3.34.2` | `CodeRedisStreamXAddFailed` | `third-party/db/writer/redis` | `third-party` | **NEW** — ExitCode 74, ADR-gated |

`Major=0` (internal), `Layer=3` (service). Each NEW code's `Public` MUST be a
plain string literal (no `fmt.Sprintf`, no concatenation) so
`TestAuditPublicIsStringLiteral` passes, and each sentinel `var` name MUST be the
code identifier minus its `Code` prefix (`CodeRotFileDecodeFailed` →
`RotFileDecodeFailed`) so `camelToScreamingSnake(varName)` matches the `reason`
literal under `TestAuditReasonMatchesVarName`. **Secret gate (ADR 0013):** a DB
or transport writer's error MUST name only the failure *kind* — never echo a DSN,
a credential, or a row value into `Public` / `Private` / `Fields`; credentials
route through the redacting `CredentialProvider` / `CredentialValue` pattern.

### D5 — The 0-alloc property belongs to the producer path, not to a batching Write

A batching writer (`dbsink`, `s3`, `cloudwatch`) hands the payload across an
async boundary, so its `Write` **MUST copy** the caller's slice
(`item := slices.Clone(p)`) — this is the deliberate ownership-transfer copy the
shipped `third-party/aws/writer/s3` sink already pays (`s3sink.go`). A batching
`Write` therefore makes **no zero-alloc claim**; CPU/RAM are kept minimal by the
batcher *coalescing* many records into one flush, not by avoiding the per-`Write`
copy. The SDK's **0-alloc invariant is scoped to the producer hot path** —
`Build().Send()` — which is measured and asserted there (WI-7), not in any
sink's `Write`. This ADR records that scoping so no downstream item is specified
with a false `allocs/op == 0` target on a batching `Write`.

## Consequences

- **A reviewer can place any new writer mechanically.** "Does it import a vendor
  SDK?" → if yes, `third-party/*`; if no, `internal/service/writer/*`. No
  judgement call, no precedent-archaeology.
- **`depTier` becomes a reviewable, documented property.** Every writer package's
  `CLAUDE.md` states its tier; `core/writer/CLAUDE.md` carries the master table.
  A dep-light regression (a vendor import sneaking into an in-tree writer) is
  caught by the existing `go list -deps ./pkg/v1/...` check, now with a written
  rule explaining *why* it must stay empty.
- **The default logger story is written down.** console + file active by default;
  rotation off until `rotfile` is selected; on rotation, compress + 24h archive +
  7-day retention, all YAML-overridable.
- **WI-9..WI-11 are unblocked.** The DB shell and the vendored DB/transport
  writers now have an Accepted ADR freezing their placement, their `depTier`,
  their codes, and the secret gate — they may proceed against a stated decision.
- **No core interface changes.** The registry, `Factory`, `Sink`, `Encoder`, and
  `RecordEvent` contracts are untouched; this ADR is taxonomy + policy + code
  reservation only.

## Why not encode `depTier` as a runtime field on `Factory`?

Because it would re-couple `core/writer` to the dependency tiers it exists to
keep out. The registry would have to carry a vendor-tier vocabulary, and a
consumer could in principle branch on it at runtime — inviting exactly the kind
of "turn on the heavy writer dynamically" coupling that the opt-in blank-import
model forbids. The tier is a **build-time, placement-time** fact; encoding it as
data adds runtime surface for zero runtime benefit. Documentation + the
`go list -deps` check is the right enforcement layer.

## Why not model DB/transport writers as codec `Format`s?

A writer is a transport, not a wire format. The `codec.Format` set is frozen and
append-only (ADR 0003/0014); a writer's job is *delivery*, not *encoding*. The
record is encoded once by the logger's `Encoder` and the resulting bytes are
handed to the `Sink` the factory produced. Adding `mysql` as a `Format` would
both break the frozen Format set and conflate two orthogonal axes.

## Why not put the DB writers in-tree behind a build tag?

Build tags do not stop `go list -deps ./pkg/v1/...` from seeing the import edge
under the default build, and they fragment the dep-light guarantee across tag
permutations. The `third-party/*` quarantine (ADR 0013) is a single, auditable
boundary: nothing requires the root/third-party module, so the dep simply cannot
reach `pkg/v1`. That is a stronger, simpler guarantee than per-tag auditing.

## Deferred

- The concrete WI-2 interval-rotation ticker (the `RotateEvery` field +
  `kernel/worker` daemon) and the YAML-reachable `rotfile` `Decoder` (WI-3) — this
  ADR freezes the policy (24h archive + 7-day retention + compress, reusing
  `CodeRotFileRotateFailed`); the ticker mechanism ships in its own file-disjoint
  item. (The default console+file constructor `DefaultMulti`, the console/file
  `Decoder`s, and the `dbsink` batching shell have **landed** alongside this ADR.)
- The WI-10/WI-11 vendored writers (`third-party/db/writer/{mysql,clickhouse,redis}`,
  `nettransport`, `journald`) — this ADR is the **gate**, not the implementation.
  Each ships against the codes and placement frozen here, after this ADR is
  Accepted. See `.claude/plans/logger-bdd-api-writers.md` for the PR-by-PR plan.
- A `mq` writer class (Kafka / NATS) — out of scope; a future ADR extends D1.
- A YAML schema document for the full writer topology — `logger.FromConfig`
  (ADR 0014) already owns the decode path; a standalone schema doc is deferred
  until the DB writers land and the option surface is complete.

## References

- ADR 0012 — logger writer registry — `./0012-logger-writer-registry.md`
- ADR 0013 — crypto domain (third-party quarantine precedent) — `./0013-sdk-crypto-domain.md`
- ADR 0014 — the verb wave (rotfile, transform, FromConfig + Decoder) — `./0014-sdk-transform-crypto-ports-config-topology.md`
- ADR 0005 — dotted-quad error codes — `./0005-sdk-error-codes-dotted-quad.md`
- `internal/core/writer/CLAUDE.md` — the writer port + the `depTier` master table
- `internal/service/writer/rotfile/CLAUDE.md` — rotation knobs + default policy
