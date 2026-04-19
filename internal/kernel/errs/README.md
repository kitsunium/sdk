# `internal/kernel/errs`

**Layer**: kernel (emitter infrastructure package; used by every other emitter in the SDK)
**Code range**: 1000-1099 (documentary meta-codes only — no exported sentinels)

SDK-wide typed error package. Every error produced by the SDK is a `*errs.Error` carrying a layered `Code`, a stable `Reason`, a wire-safe `Public` message, and a log-only `Private` message. Consumers never import this package directly — they introspect through `pkg/v1/errs`.

## Surface (for emitter packages)

```go
// Sentinel-style constructor (panics at init on invalid args).
func Define(code int, reason, public, private string, opts ...DefineOption) *Error

// Per-error overrides, passed to Define/NewError.
func WithHTTPStatus(status int) DefineOption   // default 500
func WithExitCode(code int)     DefineOption   // default 70 (EX_SOFTWARE)

// Same as Define, provided for tooling that expects a NewXxx constructor.
func NewError(code int, reason, public, private string, opts ...DefineOption) *Error

// Attach a cause to a fresh *Error. Origin wins when cause is already *Error.
type WrapParams struct{ Code int; Reason, Public, Private string }
func Wrap(cause error, params WrapParams, fields ...FieldValue) *Error

// Closed scalar union used for structured metadata.
type FieldValue struct{ /* ... */ }
func String(key, val string) FieldValue
func Int(key string, val int64) FieldValue
func Bool(key string, val bool) FieldValue
func Float(key string, val float64) FieldValue
```

Getters on `*Error`: `Code()`, `Reason()`, `Public()`, `Private()`, `Fields()`, `Layer()`, `HTTPStatus()`, `ExitCode()`, `Error()`, `Unwrap()`, `Source()`.

Package-level accessors (walk Unwrap chain):
`CodeOf / ReasonOf / PublicOf / PrivateOf / FieldsOf / LayerOf / HTTPStatusOf / ExitCodeOf / HasCode / HasReason`.

## How to declare a new sentinel

Two rules, both enforced by the AST audit in `registry_external_test.go`:

1. The Go variable name **is** the Reason in CamelCase. `WriterNil` ↔ `"WRITER_NIL"`.
2. The `public` argument MUST be a string literal (no `fmt.Sprintf`, no concatenation, no variable).

```go
// service/logger/codes.go
const CodeWriterNil int = 3101

// service/logger/errors.go
var WriterNil = errs.Define(CodeWriterNil, "WRITER_NIL",
    "Log handler requires a non-nil writer",                       // public ≤120 runes, literal
    "service/logger.NewTextHandler called with nil io.Writer")     // private (log-only)
```

Constraints (`validateDefineArgs`):
- `code >= 1000` and non-zero
- `reason` matches `^[A-Z][A-Z0-9_]*$`
- `public` ≤ 120 runes, no newline
- `private` non-empty

A violated constraint panics at package init with a grep-friendly message (`"errs.Define: invalid public [1003 INVALID_PUBLIC]: …"`).

## How to wrap an error

```go
// Cause is stdlib (context.Canceled, *os.PathError, …)
return errs.Wrap(ctx.Err(), errs.WrapParams{
    Code:    CodeCtxCancelled,
    Reason:  "CTX_CANCELLED",
    Public:  "Operation aborted due to cancellation",
    Private: "Handle invoked with cancelled context",
}, errs.String("op", "Handle"))

// Cause is already a *Error → ORIGIN WINS.
// WrapParams values are silently IGNORED; only `fields` are appended.
return errs.Wrap(inner, errs.WrapParams{}, errs.Int("attempt", 3))
```

`errors.Is(wrapped, context.Canceled)` keeps working — the stdlib cause is preserved through `Unwrap`.

## The `Error()` contract

`err.Error()` returns the neutral stable form `"[<code> <REASON>] <public>"`. It NEVER contains `Private` or `Fields`. Bubble `err.Error()` across any boundary without fearing PII leaks; use `errs.PrivateOf(err)` explicitly when you want the diagnostic string in logs.

## Do NOT

- Use `fmt.Errorf` / `errors.New` in production code — every SDK error must route through `errs.Define` or `errs.Wrap`.
- Put sensitive values in `Public`. That string lands on the wire.
- Pass a non-literal as the `public` argument — the AST audit will fail the build.
- Overload `errors.Is` semantics — use `errs.HasCode(err, code)` / `errs.HasReason(err, reason)` for code matching.

## Tests

- `error_external_test.go` — contract tests on Define / Wrap / getters / Error() invariants.
- `accessors_external_test.go` — every Of-accessor against sdk / stdlib / nil inputs.
- `field_external_test.go` + `field_internal_test.go` — the closed FieldValue union.
- `validate_internal_test.go` — whitebox coverage of every structural rule.
- `registry_external_test.go` — SDK-wide AST audit (literal public, reason = var name, code uniqueness). Fails the build on a malformed Define anywhere under `internal/` or `pkg/`.

Coverage: 97.3%.
