// Package trace — the instrumentation scope this domain publishes.
package trace

import coreotel "github.com/kitsunium/sdk/internal/core/otel"

// DefaultScopeName names this SDK's TRACE package as the instrumenting library
// when a caller declares no scope of their own.
//
// It is a separate constant from metrics' DefaultScopeName, and the difference is
// not cosmetic: an InstrumentationScope names the library that produced THIS
// signal, so stamping a span batch with `…/pkg/v1/metrics` would tell a backend
// that the metrics package emitted spans. Sharing the value would have been the
// one place where sharing the model stopped being reuse and started being a
// wrong answer — which is exactly the line ADR 0051 §Decision 2 draws, and why
// internal/core/otel takes the default as a parameter rather than owning one.
const DefaultScopeName string = "github.com/kitsunium/sdk/pkg/v1/trace"

// NormalizeScope returns the ScopeValue a Tracer publishes, filling an empty Name
// with DefaultScopeName. Version is left exactly as supplied: it is optional in
// the specification, and inventing one would be a claim about code this package
// cannot see.
func NormalizeScope(scope coreotel.ScopeValue) coreotel.ScopeValue {
	//: the shared rule, with the default only this signal may claim.
	return coreotel.NormalizeScope(scope, DefaultScopeName)
}
