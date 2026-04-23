# `pkg/v1/errs`

**Layer**: public facade · **Scope**: read-only introspection of SDK errors

The stable v1 public accessor set for inspecting errors returned by the SDK. The concrete error type, constructors, and Field helpers live in `internal/kernel/errs` and are NOT re-exported here — consumers receive `error` values from the SDK and query them through the `Of`-family functions below.

Rationale: consumers should not be able to forge SDK errors. They observe them.

## Surface

All accessors walk the `Unwrap` chain and return the deepest `*errs.Error` value encountered. Default values when no SDK error is present are documented per function.

```go
func CodeOf(err error)       (code int, ok bool)       // 0 / false if none
func ReasonOf(err error)     (reason string, ok bool)  // "" / false if none
func PublicOf(err error)     string                    // "" if none
func PrivateOf(err error)    string                    // "" if none — DIAGNOSTIC ONLY
func LayerOf(err error)      int                       // 0 if none, else 1..9
func HTTPStatusOf(err error) int                       // 500 default
func ExitCodeOf(err error)   int                       // 70 (EX_SOFTWARE) default
func HasCode(err error, code int)       bool
func HasReason(err error, reason string) bool
```

## Quick start

```go
import (
    "github.com/kitsunium/sdk/pkg/v1/errs"
    "github.com/kitsunium/sdk/pkg/v1/logger"
)

_, err := logger.NewText(logger.Config{})  // nil Writer → fails
if errs.HasCode(err, 4101) {
    // configuration problem on our side
}
fmt.Println("wire-safe message:", errs.PublicOf(err))
fmt.Println("layer:", errs.LayerOf(err))
fmt.Println("HTTP status:", errs.HTTPStatusOf(err))
```

## The Public / Private split

- **`PublicOf`** returns the wire-safe message (≤120 runes, literal, no interpolation). Send it in HTTP/gRPC responses, error pages, user-facing surfaces.
- **`PrivateOf`** returns the detailed log-only message. **DIAGNOSTIC ONLY.** Never put it in a response, an error page, or anything the end user can see. It exists so observability tooling can correlate a request-id with a detailed server-side explanation in the log backend without re-logging the entire chain.

## HTTP status policy

- `HTTPStatusOf` defaults to **500** when the error does not carry an
  explicit override. That default is deliberate for internal failures but
  it is a leak-by-default anti-pattern for domain errors that are really
  4xx (validation, not-found, conflict, authorisation). Those errors
  MUST pass `errs.WithHTTPStatus(4xx)` at `Define` time so the accessor
  surfaces the correct status.
- Code review is the only enforcement today. A linter rule that flags
  `Define(...)` calls whose Reason looks 4xx-shaped but whose options
  omit `WithHTTPStatus` is tracked for a follow-up (post-audit plan).
- Until then: when you write a new sentinel that represents a user-
  facing 4xx, do not rely on the 500 default — name the status
  explicitly.

## Semantics reminders

- **Origin wins on wrap.** An error born in `service/logger` (code `31xx`) and observed through `pkg/v1/logger` keeps its `31xx` code — the Code describes the origin, never the observation surface.
- **`errors.Is` keeps stdlib semantics.** For code / reason matching use the explicit `HasCode` / `HasReason`. `errors.Is(err, context.Canceled)` still walks the chain even when the error is wrapped by `errs.Wrap`.
- **Default HTTP / exit codes** are global (500 / 70) — there is no layer-derived heuristic. Per-error overrides come from `errs.WithHTTPStatus` / `WithExitCode` inside the emitter package.

## Do NOT

- Serialise `PrivateOf` or `FieldsOf`-equivalent data in any user-facing channel.
- Try to import `github.com/kitsunium/sdk/internal/kernel/errs`. Go's `internal` rule blocks it; and even if it did not, consumers using the internal package would couple themselves to the Field type evolution.
- Rely on specific default values beyond 500 / 70 — emitter packages may override per error.

## Tests

- `accessors_external_test.go` — end-to-end proof that a real failure path (`logger.NewText(Config{})` → `WriterRequired`) flows through every accessor with the expected values. Also covers the stdlib-only and nil cases.
