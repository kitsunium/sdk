// Package errs — construction half of the public error API.
//
// accessors.go re-exports the read-only introspection surface; this file
// re-exports the *construction* surface so an external consumer (any module
// adopting this SDK, with no internal/ access) can mint typed SDK errors
// rather than only inspecting SDK-origin ones. Together they let a downstream
// codebase migrate off fmt.Errorf / errors.New onto the SDK error model
// wholesale — every error carrying a dotted-quad Code, a wire-safe Public
// message, and a log-only Private envelope.
//
// # Construction vs. Define
//
// SDK-internal packages mint sentinels with internal/kernel/errs.Define, which
// panics at init on a malformed sentinel — safe because a build-time AST audit
// proves every Define call well-formed before the binary ships. External
// consumers get no such audit, so [New] and [Wrap] follow a runtime policy
// instead: a structural failure (bad code, non-SCREAMING_SNAKE reason,
// empty/over-long/multiline public, empty private) returns a typed validation
// error (CodeInvalidCode / Reason / Public / Private) — the result is always a
// usable, introspectable SDK error, never nil and never a panic.
//
// # Code space for third-party modules
//
// The dotted-quad MM.LL.PP.SS taxonomy (ADR 0005) assumes a coordinated Major
// (MM) octet. The SDK only ever allocates Major in the range [0, MinAppMajor)
// — 0 for internal codes, then the public semver major (1 for pkg/v1, 2 for a
// future pkg/v2, …). To stay collision-free with the SDK now and across every
// future SDK release, a consumer assigns its own codes a Major in
// [MinAppMajor, MaxMajor] (0x40–0x7F). Declare them as hex literals exactly as
// the SDK does internally:
//
//	// myapp/errcodes.go — Major 0x40 is application-owned, never SDK-issued.
//	const (
//	    CodeUserNotFound errs.Code = 0x40_01_01_01 // 64.1.1.1
//	    CodeOrderExpired errs.Code = 0x40_01_02_01 // 64.1.2.1
//	)
//
//	var ErrUserNotFound = errs.New(CodeUserNotFound, "USER_NOT_FOUND",
//	    "user not found", "lookup miss in users table")
//
// The Major ceiling is 0x7F because every Code must round-trip through a
// positive int32 (the kernel keeps the top uint32 bit clear); 64 application
// majors is far more than the SDK's semver line will ever consume.
package errs

import kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

// MinAppMajor is the lowest Major octet reserved for third-party / application
// error codes. The SDK only ever allocates Major < MinAppMajor (0 = internal,
// 1.. = the public semver major). A consumer that builds every Code with a
// Major in [MinAppMajor, MaxMajor] is guaranteed never to collide with an SDK
// code, now or in any future SDK release. See ADR 0019.
const MinAppMajor Major = 0x40 // 64

// MaxMajor is the largest legal Major octet. Codes must round-trip through a
// positive int32 (the kernel rule that keeps the deprecated int accessor from
// wrapping negative), so the top uint32 bit stays clear and the Major octet
// caps at 0x7F.
const MaxMajor Major = 0x7F // 127

// Field is a single typed key/value pair attached to an error. Build one with
// String / Int / Int64 / Bool / Float (or NewFieldValue); the zero value is
// invalid and must never be passed across the API.
type Field = kerrs.FieldValue

// WrapParams groups the metadata Wrap stamps onto the wrapping error when the
// cause is NOT already an SDK error. When the cause IS an SDK error, origin
// wins: Code/Reason/Public/Private are inherited from the cause and only
// params.Code is appended to the wrap trail (ADR 0005).
type WrapParams = kerrs.WrapParams

// Re-exported construction helpers. Grouped to satisfy the repo var-grouping
// convention while keeping each helper's godoc on its own line.
var (
	// String builds a Field holding a string value.
	String = kerrs.String

	// Int builds a Field from a plain int (widened to int64 internally),
	// matching the slog / zap Int(key, int) convention.
	Int = kerrs.Int

	// Int64 builds a Field from a 64-bit integer (no upcast at the call site).
	Int64 = kerrs.Int64

	// Bool builds a Field holding a boolean value.
	Bool = kerrs.Bool

	// Float builds a Field holding a float64 value (shortest round-trip render).
	Float = kerrs.Float

	// NewFieldValue builds a string-typed Field. Provided for tooling that
	// expects a New-prefixed factory; prefer String for the common case.
	NewFieldValue = kerrs.NewFieldValue
)

// New constructs a typed SDK error at runtime. On success the returned error
// carries code / reason / public / private plus any fields. On a structural
// failure it returns a typed validation error (CodeInvalidCode / Reason /
// Public / Private) identifying the exact rule broken — the result is always a
// non-nil, introspectable SDK error, never a panic.
//
// Arguments mirror the kernel sentinel contract:
//   - code: a dotted-quad Code; use a Major in [MinAppMajor, MaxMajor] to stay
//     collision-free with the SDK (see the package doc and ADR 0019).
//   - reason: a stable SCREAMING_SNAKE identifier (^[A-Z][A-Z0-9_]*$).
//   - public: the wire-safe message — non-empty, ≤120 runes, no newline.
//     Validated here; an over-cap or multiline public yields a typed
//     CodeInvalidPublic error rather than the error you intended.
//   - private: the log-only diagnostic envelope — non-empty, never surfaced.
//   - fields: optional structured metadata (defensively copied).
//
// HTTP status defaults to 500 and exit code to 70 (EX_SOFTWARE); a per-error
// exit override is available through Wrap's WrapParams.ExitCode.
func New(code Code, reason, public, private string, fields ...Field) error {
	//: delegate to the kernel's non-panicking runtime constructor — it returns
	//: the specific CodeInvalid* validation error on malformed input.
	return kerrs.NewRuntime(code, reason, public, private, fields...)
}

// Wrap attaches a cause to a new error with origin-wins semantics and preserves
// the chain (errors.Is / errors.Unwrap walk through it). When the cause is
// already an SDK error (directly or behind a fmt.Errorf("%w") wrap) the result
// inherits its Code/Reason/Public/Private and appends params.Code to the trail;
// otherwise params.Code becomes the origin. Bad params at runtime do not panic —
// the returned error carries a typed validation code.
//
// Declared as a function (not a var alias over the kernel Wrap) so the public
// signature returns error: the concrete *errs.Error stays unexported, never
// leaking through the facade.
func Wrap(cause error, params WrapParams, fields ...Field) error {
	//: delegate to the kernel; the explicit error return type erases the
	//: concrete *errs.Error from the public signature.
	return kerrs.Wrap(cause, params, fields...)
}
