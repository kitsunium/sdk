package otel

// normalizeScope is NormalizeScope's body: decl_gen.go writes NormalizeScope, from the
// design, as one call of it.
func normalizeScope(scope ScopeValue, defaultName string) ScopeValue {
	//: a named scope is the caller's and is never overwritten.
	if scope.Name != "" {
		//: honoured as written.
		return scope
	}
	//: the only truthful default: the library the signal belongs to.
	return ScopeValue{Name: defaultName, Version: scope.Version}
}
