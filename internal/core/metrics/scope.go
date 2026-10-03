// Package metrics — the instrumentation scope this signal publishes.
package metrics

import coreotel "github.com/kitsunium/sdk/internal/core/otel"

// DefaultScopeName names this SDK as the instrumenting library when a caller
// declares no scope of their own. An empty scope name is legal in the
// specification and useless in a backend, so the meter fills in the one thing
// it can state truthfully: which library minted the instruments.
//
// It is this signal's own constant and is deliberately not shared:
// internal/core/trace declares its own, because a scope names the library that
// produced THIS signal (ADR 0051 §Decision 2).
const DefaultScopeName string = "github.com/kitsunium/sdk/pkg/v1/metrics"

// NormalizeScope returns the ScopeValue a Meter publishes, filling an empty Name
// with DefaultScopeName. Version is left exactly as supplied.
func NormalizeScope(scope coreotel.ScopeValue) coreotel.ScopeValue {
	//: the shared rule, with the default only this signal may claim.
	return coreotel.NormalizeScope(scope, DefaultScopeName)
}
