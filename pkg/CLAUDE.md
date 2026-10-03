<!-- updated: 2026-10-03T11:00:00Z -->
# pkg/

## Purpose

The SDK's public API surface. `pkg/` is a directory of the **one SDK module**, `github.com/kitsunium/sdk`, whose `go.mod` is at the repository root (ADR 0162): consumer packages live under the `v1/` directory and are imported as `github.com/kitsunium/sdk/pkg/v1/*`, and a consumer requires `github.com/kitsunium/sdk`. Until ADR 0162 `pkg/` was a module of its own, the bare `…/pkg` (ADR 0017 — Go forbids a `/v1` module-path suffix), released as `pkg/vX.Y.Z` up to v0.17.0; those tags stay, and a go.mod still on `…/pkg` migrates with the command ADR 0162 gives. `internal/*` is blocked by Go's `internal/` rule, which is also what keeps consumers out under Bazel; the layer order inside the repository is checked on the build graph by `scripts/check-layer-deps.sh`, not by visibility (ADR 0068, amending ADR 0004).

The SDK's major is carried by **semver**, one tag `vX.Y.Z` per release: `v0.x.x` while alpha, `v1.x.x` at first stable. A future breaking change of the public surface is a `pkg/v2/` beside `pkg/v1/`, packages of the same module, coexisting with it until deprecation.

## Contents

| Major | Purpose | State |
|---|---|---|
| `v1/` | Stable public API — **87 packages** (4 top-level — `errs`, `clock`, `crypto`, `proc` — + the nested `concur/group`, `concur/singleflight`, `concur/worker`, `concur/batcher`, `concur/snapshot`, `concur/recycler`, `collections/heap`, `collections/ring`, `crypto/agree`, `crypto/hash`, `crypto/kdf`, `crypto/mac`, `crypto/password`, `crypto/sign`, `security/authz`, `security/redact`, `security/secret`, `security/session`, `security/token`, `net/server`, `net/client`, `net/tlsid`, `net/sse`, `net/websocket`, `net/static`, `observe/logger`, `observe/logger/writer`, `observe/logger/slogbridge`, `observe/metrics`, `observe/trace`, `observe/profiling`, `proc/process`, `proc/signal`, `proc/reaper`, `proc/rlimit`, `proc/cgroup`, `proc/memlimit`, `proc/ipc`, `proc/systemd/notify`, `proc/systemd/listen`, `data/codec`, `data/codec/strictjson`, `data/codec/strictjson/httpbody`, `data/codec/jsonshape`, `data/codec/jsonpatch`, `data/codec/json`, `data/codec/yaml`, `data/codec/toml`, `data/codec/bson`, `data/codec/asn1`, `data/codec/baseenc`, `data/codec/cbor`, `data/codec/csv`, `data/codec/flatbuffers`, `data/codec/form`, `data/codec/msgpack`, `data/codec/multipart`, `data/codec/ndjson`, `data/codec/pem`, `data/codec/tlv`, `data/codec/xml`, `data/transform`, `data/sql`, `data/docstore`, `data/queue`, `data/cache`, `data/vfs`, `data/semver`, `app/config`, `app/cli`, `app/i18n`, `app/validation`, `app/view`, `app/events`, `app/scheduler`, `app/statemachine`, `app/resilience`, `app/lifecycle`, `app/health`, `app/lock`, `app/id`, `app/mail`, `app/mail/spool`; `concur/`, `collections/`, `security/`, `net/`, `observe/`, `data/`, `app/` and `proc/systemd/` themselves are family directories with no Go code — ADR 0155), grouped in the root `README.md`: observability (the four `observe/` facades (`logger` + its children `writer` and `slogbridge`, `metrics`, `trace`, `profiling`), `health`), application plumbing (`config`, `lifecycle`, `cli`, `events`, `queue`, `scheduler`, `statemachine`, `cache`, `clock`, `semver`, `resilience`, `lock`, `id`), the kernel primitives published as pure aliases (ADR 0159 §4: the six `concur/` facades — `group`, `singleflight`, `worker`, `batcher`, `snapshot`, `recycler` — and the two `collections/` facades — `heap`, `ring`), network and web (the six `net/` facades (`server`, `client`, `tlsid`, `sse`, `websocket`, `static`), `view`, `i18n`, `mail` + its `spool` outbox), data and security (`codec` + its sixteen per-format packages + `strictjson` (+ `httpbody`)/`jsonshape`/`jsonpatch`, `transform`, `errs`, `crypto` + its six scheme facades as children (ADR 0155), the five `security/` facades (`authz`, `redact`, `secret`, `session`, `token`), `validation`, `sql`, `docstore`, `vfs`), and process and platform (`proc` + its nine children as the family (ADR 0155): `process`, `signal`, `reaper`, `rlimit`, `cgroup`, `memlimit`, `ipc`, and the two systemd protocols `systemd/notify` and `systemd/listen`); the distribution packages (`git`, `selfupdate`, `entitlement`, `gate`) are the framework's since ADR 0158 | Shipping |

## Versioning policy

- `pkg/v1` signatures are **frozen at the SDK's semver `v1.0.0` tag** (not the `v1/` directory name). Pre-1.0 (`v0.x.x` alpha) breaking changes are allowed; post-1.0 any breaking change goes into `pkg/v2/` (coexists with v1 until deprecation).
- Security fixes in `internal/*` ship in the SDK module's next release — no `pkg/v1` changes required because it only re-exports.
- Adding `pkg/v2/` is a dedicated ADR.

### Sizing a release — the `release:*` label (ADR 0135)

A change to `pkg/` (or to `internal/` that reaches it) cuts a **patch** of the
SDK module by default — one tag `vX.Y.Z` (ADR 0162). Anything that adds an exported symbol needs a **minor**, and the size is
set by a maintainer as a LABEL on the pull request — `release:minor` (or
`release:major`, or `release:patch` to decline a request) — before the merge,
never by text in a commit message. The release reads the label when it runs,
right after CI passes on `main`; a label added once a release is cut sizes
nothing.

    gh pr edit <number> --add-label release:minor

Why a label and not the `Release-bump:` trailer this section used to describe:
the repository squash-merges with `COMMIT_MESSAGES`, so the message the release
reads is composed from the branch commits. That text buried maintainer trailers
(`pkg/v0.1.35` and `pkg/v0.3.4` shipped as patches that had asked for a minor)
and let a contributor set the size, and nothing in a message says who wrote a
line. A label can only be applied by an account with triage or write access.

What still happens to a `Release-bump:` line:

- **It is a request, not a decision.** A merge whose message asks for more than
  a patch, with no label to decide it, STOPS the release — publishing the patch
  would lose a request somebody may have meant. The `Release size` check on the
  pull request fails for the same reason before the merge, naming the label that
  settles it. Writing the line in the merge dialog is still only a request: the
  check cannot see that text, the release job can, and it refuses.
- **A label always wins.** `release:patch` on a pull request whose commits ask
  for a minor is how a contributor's request is declined.
- **A refused release is not a deadlock.** Label the pull request the refusal
  names and re-run the failed job, or dispatch SDK Release with `bump` =
  patch/minor/major. Both are written in the refusal itself; neither is a
  hand-run `cut-tags.sh --range`.

## Conventions

- Public types are **type aliases** (clean short names, zero runtime cost). Example: `Attr = AttrValue`. What a name aliases is decided by which layer OWNS the type, and the question that decides it is: *would a second implementation of this domain's port have to accept this type?* (ADR 0074 measured 233 aliases onto core, 69 onto service and 3 onto kernel when it was written; counted again on the reorganised tree — every `type X = p.Y` in a production file of `pkg/v1`, by the layer of `p` — 290 onto core, 154 onto service, 33 onto kernel.)
  - **Yes ⇒ it lives in `internal/core/*` (or `internal/kernel/*`) and the alias points there.** Anything crossing an interface declared in core — what a method takes or returns, what a consumer implements, a value carried across the port — is owned by the CONTRACT, because every implementation must produce and accept exactly it. `corenet.IdentityParams` and `corenet.IdentityFileParams` are the pair that has to stay together for that reason.
  - **No ⇒ it belongs to the engine, and the alias points at `internal/service/*`.** That covers an engine's HANDLE (`client.Client`, `server.Server`, `server.Group`, `net/websocket.Conn`, `logger.Builder`, `i18n.Printer`), its `Option` closures over that handle's own struct (`server.Option`, `sse.Option`, `cgroup.Option`, `reaper.Option`), and — a large group — a single engine's CONSTRUCTION PARAMETERS: `sql.Config`, `session.FileConfig`, `queue.FileConfig`, `lock.FileConfig`, `health.Config`, `lifecycle.RunConfig`, `token.IssuerConfig`, `mail.SMTPConfig`, `metrics.OTLPHTTPConfig`, the five `resilience` policy configs. They name a `*sql.DB`, a directory, a file mode policy, an SMTP TLS mode — one implementation's vocabulary, which is exactly what core must not carry. Hoisting them would make the contract layer describe one backend, and a second backend would inherit fields meaning nothing to it.

  What is STILL a defect: a type the port speaks that is nevertheless declared in a service package. That puts one concept on both sides of the boundary and lets the halves drift. The count of service aliases is not the measure — the ownership question is.
- **A member of an aliased type is linked as `[Type].Member`, never `[Type.Member]`** (ADR 0138). go/doc collects methods and fields from the declarations of the package it documents, and an alias declares none, so `[Broker.Publish]` renders as literal bracketed text on pkg.go.dev and in the generated README while `[Broker].Publish` links the alias and reads the same. `make doclinks` (part of `make lint-check`) fails on the first form.
- Public functions are thin wrappers: validation + delegation. No business logic in this layer.
- **No constructors for internal types — except the error model.** The concrete `*errs.Error` type stays unexported, but since ADR 0019 the error *model* is constructable through the public facade: `pkg/v1/errs.New` / `Wrap` (+ `Field` helpers `String`/`Int`/…, `WrapParams`, `MinAppMajor`/`MaxMajor`) mint typed errors validated at runtime (returning a typed `CodeInvalid*` error, never panicking). Consumers still cannot forge an `*errs.Error` by struct literal — they go through the validated constructors. Deliberate exception: the error model is meant to be shared (downstreams migrate off `fmt.Errorf` onto it); every *other* internal type (logger handlers, codec internals) keeps its constructors private. Introspection is unchanged via `pkg/v1/errs.*Of(err)` (`CodeOf`, `ReasonOf`, `PublicOf`, `PrivateOf`, `HTTPStatusOf`, `ExitCodeOf`, `HasCode`, `HasReason`); `CodeOf` returns the typed `Code` — octets compose via `code.Layer()` / `code.Major()` / `code.Package()` / `code.Serial()`.
- **ldflags injection.** `pkg/v1/observe/logger.Version` is the single injection point; all other packages read via `logger.FrameworkVersion()`, which falls back to the `"dev"` sentinel when unset.
- **`pkg/v1/data/codec` is the aggregate of the 16 per-format packages beneath it**, each of which imports one service codec, so `import _ "github.com/kitsunium/sdk/pkg/v1/data/codec"` activates the full registry (asn1, baseenc, bson, cbor, csv, flatbuffers, form, json, msgpack, multipart, ndjson, pem, tlv, toml, xml, yaml) and `import _ "…/pkg/v1/data/codec/<format>"` one format alone, with its codes (ADR 0134). The baseenc package registers 9 distinct Format names (`base64`, `base64url`, `base32`, `base16`, `hex`, `ascii85`, `base45`, `base58`, `base62`).
- **Uniform dispatch.** Every encoding format — text, binary, base-N — is reached the same way: `codec.Marshal(format, v)` / `codec.Unmarshal(data, &v)`. Format-swap at runtime is a single string change. The SDK does NOT ship a parallel byte-level API; callers needing raw base-N bytes without the JSON envelope call stdlib `encoding/{base64,base32,hex,ascii85}` directly.

## Subtree

- `v1/` — see `pkg/v1/CLAUDE.md`

## Do NOT

- Export the concrete `*errs.Error` type here; consumers should see `error` only.
- Import from `pkg/v1` into `internal/*`. The public facade sits at the top of the dependency graph.
- Rename or remove an exported identifier in `pkg/v1/*` without adding `pkg/v2`.
- Surface `PrivateOf(err)` output in HTTP/gRPC responses — Private is diagnostic-only.
- Set `Version` at runtime from application code; use the ldflags recipe (or Bazel `--stamp`) so every binary commits its version at link time.

## Verification

```
bazel test --config=race //pkg/...
# Fallback per-module:
cd pkg && GOWORK=off go test -race -cover ./...
```
