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

// Type aliases re-export the kernel types verbatim so consumer code can
// declare variables with pkg/v1/errs.<Type> and pass them into the kernel
// API without conversion. Aliases (not new named types) preserve assignment
// compatibility — pkg/v1/errs.Code == kerrs.Code for the Go type system.
type (
	// Code is the dotted-quad error identifier packed into uint32.
	// See ADR 0005 for the registry and layout.
	Code = kerrs.Code

	// Major, Layer, PkgCode, Serial are the four octet types used by Pack.
	// Named so callers cannot swap arguments by accident.
	Major   = kerrs.Major
	Layer   = kerrs.Layer
	PkgCode = kerrs.PkgCode
	Serial  = kerrs.Serial

	// PrefixMatcher is the errors.Is target for CIDR-style Code matching.
	// Construct via NewPrefixMatcher.
	PrefixMatcher = kerrs.PrefixMatcher
)

// CIDR-style mask constants re-exported for use with NewPrefixMatcher.
const (
	// MaskByMajor matches all codes sharing the Major octet (/8 equivalent).
	MaskByMajor = kerrs.MaskByMajor
	// MaskByLayer matches all codes sharing Major+Layer (/16 equivalent).
	MaskByLayer = kerrs.MaskByLayer
	// MaskByPackage matches all codes sharing Major+Layer+Package (/24 equivalent).
	MaskByPackage = kerrs.MaskByPackage
	// MaskExact matches a single exact code (/32 equivalent).
	MaskExact = kerrs.MaskExact
)

// Re-exports of the internal accessors. Grouped to satisfy the repo-wide
// var-grouping convention while keeping each helper's godoc on its own line.
var (
	// CodeOf returns the deepest *errs.Error code in err's chain, or (0, false).
	//
	// Deprecated: use CodeValueOf for typed access. Kept at v1 for back-compat.
	CodeOf = kerrs.CodeOf

	// CodeValueOf returns the deepest typed Code in err's chain, or (0, false).
	CodeValueOf = kerrs.CodeValueOf

	// ReasonOf returns the deepest *errs.Error reason in err's chain, or ("", false).
	ReasonOf = kerrs.ReasonOf

	// PublicOf returns the deepest *errs.Error Public message, or "" when no
	// *errs.Error is in the chain.
	PublicOf = kerrs.PublicOf

	// PrivateOf returns the deepest *errs.Error Private message. DIAGNOSTIC-ONLY.
	PrivateOf = kerrs.PrivateOf

	// LayerOf returns the layer octet of the deepest *errs.Error in the chain,
	// or 0 when no *errs.Error is present.
	//
	// Deprecated: use CodeValueOf(err).Layer(). Kept at v1 for back-compat.
	LayerOf = kerrs.LayerOf

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
