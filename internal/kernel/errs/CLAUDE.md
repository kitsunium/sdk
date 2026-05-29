<!-- updated: 2026-05-18T14:30:00Z -->
# internal/kernel/errs/

## Purpose

SDK-wide typed-error meta-infrastructure. Every error returned from SDK code is a `*errs.Error` carrying a dotted-quad `Code`, a stable `Reason`, a wire-safe `Public` message, a log-only `Private` message, optional structured `Fields`, and an intrinsic wrap trail. Consumers introspect via `pkg/v1/errs` (read-only) — only emitter packages call `Define` / `Wrap`. Code range `1000-1099` is reserved for documentary meta-codes (0.0.0.1..6, never instantiated as `*Error` sentinels).

## Surface

```go
// Sentinel-style constructor (panics at init on invalid args).
func Define(code Code, reason, public, private string, opts ...DefineOption) *Error

// Per-error overrides.
func WithHTTPStatus(status int) DefineOption // default 500
func WithExitCode(code int)     DefineOption // default 70 (EX_SOFTWARE)

// Attach a cause. Origin wins when cause is already *Error.
// ExitCode (optional, default 70) sets the wrapping Error's exit status on the
// stdlib-cause path — the origin-wins path inherits the cause Error's instead.
type WrapParams struct{ Code Code; Reason, Public, Private string; ExitCode int }
func Wrap(cause error, params WrapParams, fields ...FieldValue) *Error

// Closed scalar union for structured metadata.
type FieldValue struct{ /* ... */ }
func String(key, val string) FieldValue
func Int(key string, val int64) FieldValue
func Bool(key string, val bool) FieldValue
func Float(key string, val float64) FieldValue

// Sentinel matching.
func HasCode(err error, c Code) bool                  // walks Unwrap() error AND Unwrap() []error
func HasReason(err error, reason string) bool
func NewPrefixMatcher(prefix, mask Code) *PrefixMatcher // CIDR-style; pass to errors.Is
```

Getters on `*Error`: `Code() / Reason() / Public() / Private() / Fields() / Trail() / TrailTruncated() / HTTPStatus() / ExitCode() / Error() / Unwrap() / Source()`. The Layer/Major/Package/Serial octets are reached via `e.Code().Layer()` etc. — composable on the typed `Code`.

Package-level Of-accessors walk the Unwrap chain: `CodeOf / ReasonOf / PublicOf / PrivateOf / FieldsOf / HTTPStatusOf / ExitCodeOf`. All return the typed `Code` / `string` / etc. — no int variants.

## Conventions

- **Dotted-quad `Code` (uint32, MM.LL.PP.SS).** ADR 0005. Const-expressible via hex literals (`0x01_03_02_05`); `Pack(major, layer, pkg, serial)` is runtime-only. Masks: `MaskByMajor /8`, `MaskByLayer /16`, `MaskByPackage /24`, `MaskExact /32`.
- **`Define` validation rules** (`validate.go`):
  - `code != 0` and `uint32(code) <= 0x7FFFFFFF` (int32 round-trip safety)
  - `code.Layer() != 0` unless code is one of the six meta-codes (`metaCodeAllowed` whitelist)
  - `reason` matches `^[A-Z][A-Z0-9_]*$`
  - `public` non-empty, ≤120 runes, no `\n` or `\r`
  - `private` non-empty
  - Violations panic at init with `"[<meta-code> <REASON>] <detail>"` — operators grep for the meta-code.
- **Origin-wins on `Wrap`.** When `Wrap` receives an `*errs.Error` cause (directly OR behind a stdlib wrapper, via `errors.AsType[*Error]`), the result inherits Code/Reason/Public/Private/HTTPStatus/ExitCode from the cause; only `Fields` accumulate and `params.Code` is appended to the trail. To relabel, define a fresh sentinel.
- **Runtime `Wrap` policy.** Bad `WrapParams` at runtime does NOT panic — Wrap returns `*Error{CodeInvalidWrapParams}` preserving the cause (ADR 0005 v5 HIGH fix).
- **Trail cap 16, origin-preserving truncation.** `appendTrail` keeps `[origin] + last (cap-2) entries + newest`; `trailTruncated` is monotonic. A `next == 0` is silently dropped (poison-pill defence).
- **`Error()` format (ADR 0005).** `"[<origin>[ <- <wrap1>[ <- <wrap2>...]][ (truncated)] <REASON>] <public>"`. Regex: `\[[\d.]+(?: <- [\d.]+)*(?: \(truncated\))? \w+\]`. NEVER contains Private or Fields — safe to bubble across any boundary.
- **`Is` protocol three-way dispatch.** `*PrefixMatcher` → CIDR match over origin + trail. `*Error` with non-zero Code → semantic equality by (Code, Reason). Anything else → pointer equality.
- **AST audit (`registry_external_test.go`).** Enforces three invariants across `internal/` + `pkg/`:
  1. Every `errs.Define` Public is a string literal (no `fmt.Sprintf`, no concat).
  2. Reason == `screamingSnake(varName)` (`WriterNil` ↔ `"WRITER_NIL"`).
  3. Code identifiers are unique across the SDK.
  Failure fails `bazel test //internal/kernel/errs:errs_test` (also run by `make test`).
- **Meta-codes are documentary.** 0.0.0.1..6 (`CodeInvalidCode / Reason / Public / Private / CodeString / WrapParams`) appear in Define panic messages and in `newValidationError` outputs; they are NEVER returned to callers as sentinel `*Error` values.

## How to declare a sentinel (emitter packages)

```go
// service/logger/codes.go
const CodeWriterNil errs.Code = 0x01_03_02_01 // 1.3.2.1

// service/logger/errors.go
var WriterNil = errs.Define(CodeWriterNil, "WRITER_NIL",
    "Log handler requires a non-nil writer",                       // public, literal, ≤120 runes
    "service/logger.NewTextHandler called with nil io.Writer")     // private (log-only)
```

The variable name dictates the Reason — change one, change the other; the AST audit refuses divergence.

## Do NOT

- Use `fmt.Errorf` / `errors.New` in production code — every SDK error MUST route through `Define` or `Wrap`.
- Put sensitive values in `Public`. That string lands on the wire.
- Pass a non-literal `public` to `Define` — the AST audit fails the build.
- Use `errors.Is(err, sentinel)` for code-only matching; use `errs.HasCode(err, code)` or a `NewPrefixMatcher` target for that. (`Is` does semantic match by (Code, Reason), which is fine — but the intent is clearer with `HasCode`.)
- Return a `*PrefixMatcher` from a function as `error`. It implements `error` only to satisfy `errors.Is`; escape would silently leak into error chains.

## Verification

```
bazel test --config=race //internal/kernel/errs:errs_test
# OR
cd internal/kernel && GOWORK=off go test -race -cover ./errs
# coverage target: 97.3%
```

Tests: `error_external_test.go` (Define/Wrap/getters/Error() invariants), `accessors_external_test.go` (every Of-accessor against sdk/stdlib/nil), `field_external_test.go` + `field_internal_test.go` (closed FieldValue union), `code_external_test.go` (dotted-quad packing + masks), `parse_external_test.go` (`ParseCode` string→Code), `prefix_matcher_external_test.go` (CIDR matching over origin + trail), `trail_internal_test.go` (cap + truncation), `validate_internal_test.go` (every structural rule), `registry_external_test.go` (SDK-wide AST audit).

A longer-form companion lives in `README.md`.
