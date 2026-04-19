// Package errs exposes read-only introspection of SDK errors to external
// consumers. The concrete error type, constructors, and Field helpers live
// in internal/kernel/errs — consumers receive errors from the SDK and
// query them via the Of-accessors re-exported here. This keeps callers
// from forging SDK errors while still enabling dashboards, retries, and
// structured logs to branch on Code / Reason / HTTPStatus / ExitCode.
//
// PrivateOf is exposed intentionally for operator diagnostics. DIAGNOSTIC
// ONLY — its output must never land in HTTP/gRPC responses, error pages,
// or any user-facing surface. Use Public for those channels.
package errs

import kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

// Re-exports of the internal Of-family accessors. Grouped to satisfy the
// repo-wide var-grouping convention while keeping each helper's godoc
// on its own line for tooling.
var (
	// CodeOf returns the deepest *errs.Error code in err's chain, or (0, false).
	CodeOf = kerrs.CodeOf

	// ReasonOf returns the deepest *errs.Error reason in err's chain, or ("", false).
	ReasonOf = kerrs.ReasonOf

	// PublicOf returns the deepest *errs.Error Public message, or "" when no
	// *errs.Error is in the chain.
	PublicOf = kerrs.PublicOf

	// PrivateOf returns the deepest *errs.Error Private message. DIAGNOSTIC-ONLY.
	PrivateOf = kerrs.PrivateOf

	// LayerOf returns the originating layer (1..9) of the deepest *errs.Error
	// in the chain, or 0 when unclassified.
	LayerOf = kerrs.LayerOf

	// HTTPStatusOf returns the HTTP status mapped from the deepest *errs.Error,
	// defaulting to 500 when no *errs.Error is present.
	HTTPStatusOf = kerrs.HTTPStatusOf

	// ExitCodeOf returns the sysexits code mapped from the deepest *errs.Error,
	// defaulting to 70 (EX_SOFTWARE) when no *errs.Error is present.
	ExitCodeOf = kerrs.ExitCodeOf

	// HasCode walks the chain and reports whether any *errs.Error carries code.
	HasCode = kerrs.HasCode

	// HasReason walks the chain and reports whether any *errs.Error carries reason.
	HasReason = kerrs.HasReason
)
