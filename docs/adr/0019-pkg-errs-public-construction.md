# ADR 0019 — Public error construction API + third-party `MM` (Major) code assignment

**Status**: Accepted
**Date**: 2026-06-15
**Deciders**: kitsunium maintainers
**Supersedes**: —
**Amends**: ADR 0002 (the error model is now consumer-constructable, not only consumer-inspectable), ADR 0005 (reserves a Major range for third-party codes)
**Related**: ADR 0017 (the public module these symbols ship in), ADR 0004 (Bazel visibility keeps the concrete `*Error` type internal)

## Context

`pkg/v1/errs` shipped as a **read-only** facade: consumers could introspect an
SDK-origin error (`CodeOf`, `PublicOf`, `HasCode`, …) but could not *construct*
one. The construction half — `Define` / `Wrap` / the `Field` helpers — lived in
`internal/kernel/errs`, which Go's `internal/` rule makes importable only by
packages under `github.com/kitsunium/sdk/…`. The package doc said so plainly:
construction is "intentionally NOT re-exported."

That made the SDK's error model effectively SDK-private. The model's whole
premise (ADR 0002) is that `fmt.Errorf` / `errors.New` are banned and every
error carries a dotted-quad `Code` + a wire-safe `Public` + a log-only
`Private`. A downstream module could *observe* that model on errors it received
but could not *participate* in it — it had to keep using stdlib errors for
everything it raised itself. Any real adoption (e.g. supervizio/agent's planned
migration of ~246 `fmt.Errorf` sites onto the SDK model) was blocked on the
construction surface being public.

Two design problems had to be solved together:

1. **Construction without the AST audit.** SDK-internal sentinels go through
   `errs.Define`, which *panics* at init on a malformed sentinel. That is safe
   internally because a build-time AST audit
   (`//internal/kernel/errs:errs_test`) proves every `Define` call well-formed
   (literal `Public`, `reason == screamingSnake(varName)`, unique code) before
   the binary ships. **External consumers get no such audit**, so a re-exported
   `Define` would turn a consumer's typo into a runtime panic.

2. **Code-space collision.** The dotted-quad `MM.LL.PP.SS` taxonomy (ADR 0005)
   assumes a *coordinated* Major (`MM`) octet. The SDK allocates `MM = 0`
   (internal) and `MM = 1` (public, pkg/v1). If two independent SDK consumers
   both mint codes starting at `MM = 1`, their catalogs collide with each other
   and with the SDK. There must be a documented rule for which Major bytes a
   third party may use.

## Decision

### 1. Re-export construction through a *runtime-validated*, non-panicking path

`pkg/v1/errs` gains a construction surface (in `construct.go`):

```go
func New(code Code, reason, public, private string, fields ...Field) error
var  Wrap = kerrs.Wrap                 // (cause, WrapParams, fields...) error
type WrapParams = kerrs.WrapParams
type Field      = kerrs.FieldValue
var  String, Int, Int64, Bool, Float, NewFieldValue = …   // Field constructors
const MinAppMajor Major = 0x40
const MaxMajor    Major = 0x7F
```

`New` is backed by a new kernel constructor, `errs.NewRuntime`, that mirrors
`Wrap`'s existing runtime policy (ADR 0005 v5 HIGH fix): a structural failure
(bad code, non-`SCREAMING_SNAKE` reason, empty/over-120-rune/multiline public,
empty private) returns the **specific** typed validation error
(`CodeInvalidCode` / `Reason` / `Public` / `Private`) instead of panicking. The
result is **always** a usable, introspectable SDK error — never nil, never a
panic. `Define` stays the internal, AST-audited, panic-at-init path; consumers
never touch it.

The concrete `*errs.Error` type stays unexported through the facade — `New` and
`Wrap` return `error`. Consumers cannot forge an error by struct literal; they
go through the validated constructors. This preserves the ADR 0002 invariant
(no raw `errors.New`) on the consumer side *by construction*, while letting them
own their own catalog.

### 2. Reserve Major `0x40`–`0x7F` for third-party / application codes

The SDK commits to allocating **only** `MM < 0x40` for its own codes:

| Major range | Owner | Examples |
|---|---|---|
| `0x00`         | SDK — internal codes | `0.2.6.*` (proc), `0.3.1.*` (logger) |
| `0x01 .. 0x3F` | SDK — public semver major | `1.x.x.x` (pkg/v1); a future pkg/v2 → `2.*` |
| `0x40 .. 0x7F` | **Third-party / application** | `0x40_01_01_01` = 64.1.1.1 |

`MinAppMajor = 0x40` (64) is the reserved boundary; `MaxMajor = 0x7F` (127) is
the legal ceiling, because every `Code` must round-trip through a **positive
int32** (the kernel keeps the top `uint32` bit clear so the deprecated int
accessor never wraps negative — `validateCode` enforces `code <= 0x7FFFFFFF`).

A consumer assigns its codes a Major in `[MinAppMajor, MaxMajor]` and is
guaranteed collision-free with the SDK now and across every future SDK release.
64 application majors is far more than the SDK's single semver line will ever
consume, and far more than any one application needs — multiple consumers can
still collide *with each other* if they all pick `0x40`, but that is the
application ecosystem's coordination problem, not the SDK's; the SDK only
guarantees it will never itself issue a code `≥ 0x40`. Consumers declare codes
as hex literals exactly as the SDK does internally:

```go
const CodeUserNotFound errs.Code = 0x40_01_01_01 // 64.1.1.1
var  ErrUserNotFound          = errs.New(CodeUserNotFound, "USER_NOT_FOUND",
    "user not found", "lookup miss in users table")
```

## Consequences

- **The error model is now adoptable end-to-end.** A downstream module can
  migrate every `fmt.Errorf` / `errors.New` site onto `errs.New` / `errs.Wrap`,
  inheriting the Public/Private split, dotted-quad routing, `HasCode`/`Is`
  matching, and HTTP/exit mapping — using only the public module.
- **`pkg/v1` reverses its "no constructors for internal types" rule** for `errs`
  specifically. The rule stands for every other internal type (logger handlers,
  codec internals stay constructor-private); `errs` is the deliberate exception
  because the error *model*, unlike a concrete handler, is meant to be shared.
- **No new AST-audit burden.** The audit targets `errs.Define` call sites only;
  `New`/`Wrap`/`NewRuntime` are validated at runtime, so a consumer's dynamic
  `Public` string is checked when the error is built, not at SDK build time.
- **Validation parity.** The same `validate.go` rules (≤120-rune newline-free
  public, `SCREAMING_SNAKE` reason, well-formed code) apply to consumer-built
  errors — they just surface as a returned typed error rather than an init
  panic.
- **HTTP-status override is not yet on `New`.** `New` defaults HTTP 500 / exit
  70; a per-error exit override is reachable via `Wrap`'s `WrapParams.ExitCode`.
  A `WithHTTPStatus`-style option on `New` is deferred until consumer demand
  surfaces (consistent with the package's "add on demand" posture, e.g. the
  still-deferred `FieldsOfAsMap`).

## Alternatives considered

- **Re-export `Define` verbatim.** Rejected: it panics at init, and external
  callers have no AST audit to catch malformed sentinels before runtime — a
  consumer typo would crash their program at startup.
- **Build `New` on `Wrap(nil, WrapParams{…})`.** Workable (that path already
  validates and never panics) but a `New` misuse would surface as
  `INVALID_WRAP_PARAMS` — confusing when nothing was wrapped. `NewRuntime`
  returns the precise `INVALID_PUBLIC` / `INVALID_REASON` / … verdict instead.
- **A single fixed application Major (e.g. `0x7F`).** Rejected: it gives a
  consumer one layer×package×serial space and forces every application onto the
  same Major, maximising cross-application collisions. A range lets a consumer
  shard its own catalog across majors.
- **A consumer-local registry object** (codes scoped to a handle, not global).
  Rejected as over-engineered for v1: the hex-literal + reserved-range
  convention matches how the SDK itself declares codes and needs no new type.
```
