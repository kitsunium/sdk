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
