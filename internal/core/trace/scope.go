// Package trace — the instrumentation scope this domain publishes.
package trace

import coremetrics "github.com/kitsunium/sdk/internal/core/metrics"

// DefaultScopeName names this SDK's TRACE package as the instrumenting library
// when a caller declares no scope of their own.
//
// It is a separate constant from metrics' DefaultScopeName, and the difference is
// not cosmetic: an InstrumentationScope names the library that produced THIS
// signal, so stamping a span batch with `…/pkg/v1/metrics` would tell a backend
// that the metrics package emitted spans. Reusing the value would have been the
// one place where sharing the metrics types stopped being reuse and started being
// a wrong answer — which is exactly the line ADR 0051 §Decision 2 draws.
const DefaultScopeName string = "github.com/kitsunium/sdk/pkg/v1/trace"

// NormalizeScope returns the ScopeValue a Tracer publishes, filling an empty Name
// with DefaultScopeName. Version is left exactly as supplied: it is optional in
// the specification, and inventing one would be a claim about code this package
// cannot see.
func NormalizeScope(scope coremetrics.ScopeValue) coremetrics.ScopeValue {
	//: a named scope is the caller's and is never overwritten.
	if scope.Name != "" {
		//: honoured as written.
		return scope
	}
	//: the only truthful default: the library that started the spans.
	return coremetrics.ScopeValue{Name: DefaultScopeName, Version: scope.Version}
}
