<!-- updated: 2026-05-18T14:30:00Z -->
# pkg/

## Purpose

The SDK's public API surface. It is a **single Go module** — the bare `github.com/kitsunium/sdk/pkg` (`go.mod` at `pkg/go.mod`); Go forbids a `/v1` module-path suffix, so the module cannot be `…/pkg/v1` (ADR 0017). Consumer packages live under the `v1/` directory and are imported as `pkg/v1/*`; the `v1/` is a directory, not a separate module. `internal/*` is blocked by Go's `internal/` rule AND by Bazel layer visibility (ADR 0004).

The module's major is carried by **semver**: `v0.x.x` while alpha, `v1.x.x` at first stable. A future breaking change becomes a real second module `…/pkg/v2` (legal `/v2` suffix, `go.mod` at `pkg/v2/`), coexisting with this one.

## Contents

| Major | Purpose | State |
|---|---|---|
| `v1/` | Stable public API for logging, error introspection, and codec dispatch (16 codecs covering 24 Format names — incl. base-N family via uniform `codec.Marshal`/`Unmarshal`) | Shipping |

## Versioning policy

- `pkg/v1` signatures are **frozen at the semver `v1.0.0` tag** (not the `v1/` directory name). Pre-1.0 (`v0.x.x` alpha) breaking changes are allowed; post-1.0 any breaking change goes into a new `…/pkg/v2` module (coexists with v1 until deprecation).
- Security fixes in `internal/*` propagate via minor bumps on the module concerned — no `pkg/v1` changes required because it only re-exports.
- Adding a `…/pkg/v2` module is a dedicated ADR.

## Conventions

- Public types are **type aliases** (clean short names, zero runtime cost). Example: `Attr = AttrValue`. What a name aliases is decided by which layer OWNS the type, and the question that decides it is: *would a second implementation of this domain's port have to accept this type?* (ADR 0074 — measured: 233 aliases onto core, 69 onto service, 3 onto kernel.)
  - **Yes ⇒ it lives in `internal/core/*` (or `internal/kernel/*`) and the alias points there.** Anything crossing an interface declared in core — what a method takes or returns, what a consumer implements, a value carried across the port — is owned by the CONTRACT, because every implementation must produce and accept exactly it. `corenet.IdentityParams` and `corenet.IdentityFileParams` are the pair that has to stay together for that reason.
  - **No ⇒ it belongs to the engine, and the alias points at `internal/service/*`.** That covers an engine's HANDLE (`client.Client`, `server.Server`, `server.Group`, `net/websocket.Conn`, `logger.Builder`, `i18n.Printer`), its `Option` closures over that handle's own struct (`server.Option`, `sse.Option`, `cgroup.Option`, `reaper.Option`), and — the largest group, about forty of the 69 — a single engine's CONSTRUCTION PARAMETERS: `sql.Config`, `session.FileConfig`, `queue.FileConfig`, `lock.FileConfig`, `health.Config`, `lifecycle.RunConfig`, `token.IssuerConfig`, `mail.SMTPConfig`, `metrics.OTLPHTTPConfig`, the five `resilience` policy configs. They name a `*sql.DB`, a directory, a file mode policy, an SMTP TLS mode — one implementation's vocabulary, which is exactly what core must not carry. Hoisting them would make the contract layer describe one backend, and a second backend would inherit fields meaning nothing to it.

  What is STILL a defect: a type the port speaks that is nevertheless declared in a service package. That puts one concept on both sides of the boundary and lets the halves drift. The count of service aliases is not the measure — the ownership question is.
- Public functions are thin wrappers: validation + delegation. No business logic in this layer.
- **No constructors for internal types — except the error model.** The concrete `*errs.Error` type stays unexported, but since ADR 0019 the error *model* is constructable through the public facade: `pkg/v1/errs.New` / `Wrap` (+ `Field` helpers `String`/`Int`/…, `WrapParams`, `MinAppMajor`/`MaxMajor`) mint typed errors validated at runtime (returning a typed `CodeInvalid*` error, never panicking). Consumers still cannot forge an `*errs.Error` by struct literal — they go through the validated constructors. Deliberate exception: the error model is meant to be shared (downstreams migrate off `fmt.Errorf` onto it); every *other* internal type (logger handlers, codec internals) keeps its constructors private. Introspection is unchanged via `pkg/v1/errs.*Of(err)` (`CodeOf`, `ReasonOf`, `PublicOf`, `PrivateOf`, `HTTPStatusOf`, `ExitCodeOf`, `HasCode`, `HasReason`); `CodeOf` returns the typed `Code` — octets compose via `code.Layer()` / `code.Major()` / `code.Package()` / `code.Serial()`.
- **ldflags injection.** `pkg/v1/logger.Version` is the single injection point; all other packages read via `logger.FrameworkVersion()`, which falls back to the `"dev"` sentinel when unset.
- **`pkg/v1/codec` blank-imports all 16 service codec packages** so `import _ "github.com/kitsunium/sdk/pkg/v1/codec"` activates the full registry (asn1, baseenc, bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml). The baseenc package registers 9 distinct Format names (`base64`, `base64url`, `base32`, `base16`, `hex`, `ascii85`, `base45`, `base58`, `base62`).
- **Uniform dispatch.** Every encoding format — text, binary, base-N — is reached the same way: `codec.Marshal(format, v)` / `codec.Unmarshal(data, &v)`. Format-swap at runtime is a single string change. The SDK does NOT ship a parallel byte-level API; callers needing raw base-N bytes without the JSON envelope call stdlib `encoding/{base64,base32,hex,ascii85}` directly.

## Subtree

- `v1/` — see `pkg/v1/CLAUDE.md`

## Do NOT

- Export the concrete `*errs.Error` type here; consumers should see `error` only.
- Import from `pkg/v1` into `internal/*`. The public facade sits at the top of the dependency graph.
- Rename or remove an exported identifier in `pkg/v1/*` without cutting `pkg/v2`.
- Surface `PrivateOf(err)` output in HTTP/gRPC responses — Private is diagnostic-only.
- Set `Version` at runtime from application code; use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.

## Verification

```
bazel test --config=race //pkg/...
# Fallback per-module:
cd pkg && GOWORK=off go test -race -cover ./...
```
