//go:generate gomarkdoc --output README.md .

// Package errs is the read-only introspection facade for SDK errors.
//
// The concrete error type, constructors ([github.com/kitsunium/sdk/internal/kernel/errs].Define,
// Wrap), and Field helpers live in internal/kernel/errs and are
// intentionally NOT re-exported. Consumers receive [error] values from
// the SDK and query them via the Of-family accessors below. This keeps
// callers from forging SDK errors while still enabling dashboards,
// retries, and structured logs to branch on Code / Reason / HTTPStatus
// / ExitCode.
//
// # Surface
//
// Accessors walk the Unwrap chain (both `Unwrap() error` and
// `Unwrap() []error`) and return the deepest *errs.Error value
// encountered. Defaults document the behaviour when no SDK error is
// present:
//
//	func CodeOf(err error)       (Code, bool)    // 0 / false if none — typed dotted-quad
//	func ReasonOf(err error)     (string, bool)  // "" / false if none
//	func PublicOf(err error)     string          // "" if none
//	func PrivateOf(err error)    string          // "" if none — DIAGNOSTIC ONLY
//	func HTTPStatusOf(err error) int             // 500 default
//	func ExitCodeOf(err error)   int             // 70 (EX_SOFTWARE) default
//	func HasCode(err error, c Code) bool
//	func HasReason(err error, reason string) bool
//
// Octets are reached on the typed [Code] itself: code.Major(),
// code.Layer(), code.Package(), code.Serial(). No separate LayerOf
// accessor exists — the kernel exports exactly one accessor per field
// and this package re-exports it verbatim.
//
// # Quick start
//
//	import (
//	    "github.com/kitsunium/sdk/pkg/v1/errs"
//	    "github.com/kitsunium/sdk/pkg/v1/logger"
//	)
//
//	_, err := logger.NewText(logger.Config{})  // nil Writer → fails
//	// 0x01_01_00_01 = 1.1.0.1 (pkg/v1/logger WriterRequired under ADR 0005).
//	if errs.HasCode(err, 0x01_01_00_01) {
//	    // configuration problem on our side
//	}
//	fmt.Println("wire-safe message:", errs.PublicOf(err))
//	if code, ok := errs.CodeOf(err); ok {
//	    fmt.Println("layer:", code.Layer())
//	}
//	fmt.Println("HTTP status:", errs.HTTPStatusOf(err))
//
// # The Public/Private split
//
// [PublicOf] returns the wire-safe message (≤120 runes, literal, no
// interpolation). Send it in HTTP/gRPC responses, error pages, and
// user-facing surfaces.
//
// [PrivateOf] returns the detailed log-only message. DIAGNOSTIC ONLY.
// Never put it in a response, an error page, or anything the end user
// can see. It exists so observability tooling can correlate a request
// ID with a detailed server-side explanation in the log backend
// without re-logging the entire chain.
//
// # HTTP status policy
//
// [HTTPStatusOf] defaults to 500 when the error does not carry an
// explicit override. That default is deliberate for internal failures
// but it is a leak-by-default anti-pattern for domain errors that are
// really 4xx (validation, not-found, conflict, authorisation). Such
// errors MUST pass errs.WithHTTPStatus(4xx) at Define time so the
// accessor surfaces the correct status.
//
// # Semantics reminders
//
//   - Origin wins on wrap. An error born in service/logger (code 31xx) and
//     observed through pkg/v1/logger keeps its 31xx code — the Code
//     describes the origin, never the observation surface.
//   - errors.Is keeps stdlib semantics. For code / reason matching use
//     the explicit [HasCode] / [HasReason] helpers; errors.Is(err,
//     context.Canceled) still walks the chain through errs.Wrap.
//   - Default HTTP / exit codes are global (500 / 70). Per-error
//     overrides come from errs.WithHTTPStatus / WithExitCode inside the
//     emitter package.
//
// # PrefixMatcher routing
//
// [NewPrefixMatcher] returns a sentinel that classifies any wrapped
// error by Code prefix when used through errors.Is. Combine with
// [MaskByMajor] / [MaskByLayer] / [MaskByPackage] / [MaskExact] for
// CIDR-style code routing in dashboards / middleware. Single-code
// matching uses [HasCode] which is cheaper.
package errs

import kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

// Type aliases re-export the kernel types verbatim so consumer code can
// declare variables with pkg/v1/errs.<Type> and pass them into the kernel
// API without conversion. Aliases (not new named types) preserve assignment
// compatibility — pkg/v1/errs.Code == kerrs.Code for the Go type system.
type (
	// Code is the dotted-quad error identifier packed into uint32.
	// See ADR 0005 for the registry and layout.
	Code = kerrs.Code

	// Major is the top octet of Code — SemVer major version (0 = internal,
	// 1 = v1,...). See ADR 0005.
	Major = kerrs.Major
	// Layer is the second octet of Code — SDK layer (0 = meta, 1 = kernel,
	// 2 = core, 3 = service,...). See ADR 0005.
	Layer = kerrs.Layer
	// PkgCode is the third octet of Code — per-layer package slot. See ADR 0005 / 0006.
	PkgCode = kerrs.PkgCode
	// Serial is the low octet of Code — per-package serial. See ADR 0005.
	Serial = kerrs.Serial

	// PrefixMatcher is the errors.Is target for CIDR-style Code matching.
	// Construct via NewPrefixMatcher.
	PrefixMatcher = kerrs.PrefixMatcher
)

// CIDR-style mask constants re-exported for use with NewPrefixMatcher.
const (
	// MaskByMajor matches all codes sharing the Major octet (/8 equivalent).
	MaskByMajor Code = kerrs.MaskByMajor
	// MaskByLayer matches all codes sharing Major+Layer (/16 equivalent).
	MaskByLayer Code = kerrs.MaskByLayer
	// MaskByPackage matches all codes sharing Major+Layer+Package (/24 equivalent).
	MaskByPackage Code = kerrs.MaskByPackage
	// MaskExact matches a single exact code (/32 equivalent).
	MaskExact Code = kerrs.MaskExact
)

// Re-exports of the internal accessors. Grouped to satisfy the repo-wide
// var-grouping convention while keeping each helper's godoc on its own line.
var (
	// CodeOf returns the deepest *errs.Error's typed Code in err's chain,
	// or (0, false) when no *errs.Error is present.
	CodeOf = kerrs.CodeOf

	// ReasonOf returns the deepest *errs.Error reason in err's chain, or ("", false).
	ReasonOf = kerrs.ReasonOf

	// PublicOf returns the deepest *errs.Error Public message, or "" when no
	// *errs.Error is in the chain.
	PublicOf = kerrs.PublicOf

	// PrivateOf returns the deepest *errs.Error Private message. DIAGNOSTIC-ONLY.
	PrivateOf = kerrs.PrivateOf

	// HTTPStatusOf returns the HTTP status mapped from the deepest *errs.Error,
	// defaulting to 500 when no *errs.Error is present.
	HTTPStatusOf = kerrs.HTTPStatusOf

	// ExitCodeOf returns the sysexits code mapped from the deepest *errs.Error,
	// defaulting to 70 (EX_SOFTWARE) when no *errs.Error is present.
	ExitCodeOf = kerrs.ExitCodeOf

	// HasCode walks the chain (including errors.Join subtrees) and reports
	// whether any *errs.Error carries code (origin or trail entry).
	HasCode = kerrs.HasCode

	// HasReason walks the chain and reports whether any *errs.Error carries reason.
	HasReason = kerrs.HasReason

	// NewPrefixMatcher builds an errors.Is target for CIDR-style Code matching.
	NewPrefixMatcher = kerrs.NewPrefixMatcher

	// Pack constructs a Code from its four octets. Runtime only — sentinel
	// constants in caller code MUST use hex literals to stay const-expressible.
	Pack = kerrs.Pack

	// ParseCode parses the canonical "M.L.P.S" textual form.
	ParseCode = kerrs.ParseCode
)
