# internal/core/writer/

## Purpose

Declares the **transport-factory port** of the logger: a named, config-driven
constructor (`Factory`) that yields a `core/logger.Sink`, plus the process-wide
registry mapping a writer `Name` to its `Factory`. Peer of
`internal/core/codec` — the registry resolves a `Name` to a `Factory` exactly
as codec resolves a `Format` to a `Codec` (ADR 0012).

A writer is **not** a transport — it builds one. `Factory.Open` runs once at
logger-construction time and returns a `Sink`; the hot path (`Sink.Write`) is
untouched by this package. `Sink` is **not** renamed and the
`internal/service/logger/sink/*` tree is unchanged — `writer` sits beside the
logger, it does not replace any of it.

Code range: `0.2.3.*` (ADR 0012).

## Contents

| File | Surface |
|---|---|
| `writer.go`   | `Name` (typed key + `String`/`Known`), `Config = any`, `Factory` interface (`Name` / `Open`) |
| `writer_spec.go` | `Spec` value type (`Name` + `Config`); re-exported as `logger.WriterSpec` |
| `config_decoder.go` | `Decoder` **optional** Factory extension (`Decode(map[string]any) (Config, error)`) — mirrors codec's `Appender`; detected by type assertion (ADR 0014 §D5) |
| `registry.go` | `snapshot.Value[map[Name]Factory]` registry: `Register` / `Lookup` / `Open` / `Available` (mirrors `core/codec/registry.go`) |
| `codes.go`    | `CodeDuplicateRegistration` (0.2.3.1), `CodeWriterUnknownName` (0.2.3.2), `CodeWriterConfigInvalid` (0.2.3.3), `CodeWriterNil` (0.2.3.4), `CodeWriterNameEmpty` (0.2.3.5) |
| `errors.go`   | `WriterUnknownName` + the shared `WriterConfigInvalid` sentinel (the latter returned by every factory on a wrong-type `Config`) |

## `Decoder` optional extension (ADR 0014 §D5)

`Decoder` is an **optional** Factory extension — the writer peer of codec's
`Appender`. A Factory MAY implement `Decode(map[string]any) (Config, error)` to
translate a raw, codec-decoded option map (from a config file) into its typed
`Config`. The interface is single-method (named per the `-er` convention,
`KTN-INTERFACE-ERNAME`); the ADR's working name `ConfigDecoder` collapses to
`Decoder` here. Topology builders (`pkg/v1/logger.FromConfig`) type-assert each
resolved Factory to `Decoder`: when present, `Decode` owns the translation of its
own option keys; when absent, the builder falls back to a **default mapping**
that hands the raw `map[string]any` straight through as the opaque `Config = any`
(the factory's own `Open` type-assertion then accepts the map or returns
`WriterConfigInvalid`). No new error code: the interface carries no sentinel of
its own. **Secret gate:** a `Decode` that parses credentials out of the map MUST
NOT echo any option value into the error it returns — name only the failure
kind.

## `depTier` — a first-class writer property (ADR 0015)

Every writer has exactly one **`depTier`** — the property that tells a consumer
what turning the writer on costs the dependency graph. It is **documented, not
encoded**: a column in the table below and a sentence in each writer package's
`CLAUDE.md`, never a Go field on `Factory` (encoding it would re-couple core to
the very vendor tiers the tier exists to quarantine — ADR 0015 §"Why not encode
`depTier`…"). Three values:

| `depTier` | The writer's package imports… | Where it lives | Activation |
|---|---|---|---|
| `stdlib` | only the Go stdlib (+ kernel/core/service) | `internal/service/writer/*` (in-tree) | blank-import of the in-tree package, wired by `pkg/v1/logger` |
| `vendor-root` | a vendor dep already in the **root** `go.mod` (nothing requires root) | `third-party/*` (root module) | opt-in blank import of the `third-party/*` package |
| `third-party` | a vendor SDK quarantined so it never reaches `pkg/v1` | `third-party/*` (root module or own module) | opt-in blank import of the `third-party/*` package |

**The never-in-tree rule (mechanical placement):** a writer whose `depTier` is
anything other than `stdlib` MUST NOT have its factory package under
`internal/service/writer/*`. The in-tree tree is **stdlib-only by
construction**, so `go list -deps ./pkg/v1/...` is provably free of vendor SDKs
without auditing every file. Enforced by the existing dep-light check (zero
`x/crypto` / cloud / DB SDKs in `pkg/v1` deps) + Bazel visibility — not a new
test.

**Writer taxonomy (classes are documentation, not code — there is no class enum,
field, or per-class interface; the registry stays a flat `Name → Factory` map):**

| Class | Name(s) | Placement | depTier |
|---|---|---|---|
| console | `"console"` | `internal/service/writer/console` | `stdlib` |
| file | `"file"`, `"rotfile"` | `internal/service/writer/{file,rotfile}` | `stdlib` |
| transport | `"net"`, `"journald"` | `internal/service/writer/{nettransport,journald}` | `stdlib` |
| api | `"s3"`, `"cloudwatch"` | `third-party/aws/writer/{s3,cloudwatch}` | `third-party` |
| db | `"mysql"`, `"clickhouse"`, `"redis-stream"` | `third-party/db/writer/{mysql,clickhouse,redis}` | `third-party` |

A `Decoder` (above) makes a writer's knobs **YAML-reachable** regardless of its
tier. Adding a class is an ADR-level act (ADR 0015 §D1).

### Middleware placement (transport behaviour is NOT a Factory concern)

Batching, retries, async fan-out, encrypt-at-rest, rotation, and network I/O are
**middleware / Sink behaviour**, not `Factory` behaviour. `Factory.Open` runs
once and returns a `Sink`; everything stateful lives in the `Sink` it produces or
in a reused `internal/service/logger/middleware/*`. A batching Sink that hands
the payload across an async boundary **MUST copy** the caller's slice
(`slices.Clone(p)` in `Write`) — the deliberate ownership-transfer copy the
shipped `third-party/aws/writer/s3` sink already pays. Such a `Write` therefore
makes **no 0-alloc claim**; the SDK's 0-alloc invariant is scoped to the producer
hot path (`Build().Send()`), never to a batching `Write` (ADR 0015 §D5).

## Conventions

- **`snapshot.Value`, not `sync.Map`** — factories register once at import, then
  it is read-many (ADR 0011). `Register` publishes via `Value.Update` (mutex-
  serialised); `Lookup` is lock-free.
- **Registration is a package-level `var`, never `init()`** (`KTN-FUNC-NOINIT`):
  `var Writer = writer.Register(&fileFactory{})` in each concrete package.
- **Idempotent re-registration** of the same `Name` is fine; a *distinct*
  factory claiming a taken `Name` **panics at boot** with the dotted-quad code.
- **`Name("")` is the reserved invalid zero value** — `Known()` is false,
  `Lookup` always misses.
- **One shared `WriterConfigInvalid`** — factories return it (with an `errs`
  Field naming the writer) instead of minting per-package config-error codes.
- **IFACE-PLUGIN.** `Register` / `Lookup` hand factory instances back behind the
  `Factory` interface; concrete factory types stay unexported in their packages.

## Do NOT

- Add a `MustRegister` variant — `Register` already panics on duplicate/nil.
- Add deregistration / replacement — the registry is append-only by contract.
- Import a service- or pkg-layer writer from here. Concrete factories register
  themselves on import; core stays unaware of which writers exist.
- Put transport behaviour (batching, retries, network I/O) in a `Factory` — that
  belongs in the `Sink` it produces (or a reused middleware).
- Place a non-`stdlib` `depTier` writer under `internal/service/writer/*` — vendor-
  backed writers live in `third-party/*` and are reached by opt-in blank import
  only (the never-in-tree rule, ADR 0015 §D2).
- Encode `depTier` as a `Factory` field — it is documented, never runtime data
  (ADR 0015 §"Why not encode `depTier`…").

## Verification

```bash
bazel test --config=race //internal/core/writer:writer_test
# Fallback
cd internal/core && GOWORK=off go test -race -cover ./writer/...
```
