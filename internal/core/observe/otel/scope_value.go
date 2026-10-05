package otel

// NormalizeScope returns scope with an empty Name filled by defaultName.
// Version is left exactly as supplied.
//
// The default is a PARAMETER because it is the one part of the shared model
// that must not be shared: a scope names the library that produced THIS
// signal, so a span batch stamped with the metrics package's name would tell a
// backend that the metrics package emitted spans. Each signal passes its own
// DefaultScopeName (ADR 0051 §Decision 2).
func NormalizeScope(scope ScopeValue, defaultName string) ScopeValue {
	//: a named scope is the caller's and is never overwritten.
	if scope.Name != "" {
		//: honoured as written.
		return scope
	}
	//: the only truthful default: the library the signal belongs to.
	return ScopeValue{Name: defaultName, Version: scope.Version}
}
