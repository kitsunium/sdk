package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// scopeOf is ScopeOf's body: decl_gen.go writes ScopeOf, from the
// design, as one call of it.
func scopeOf(scope coreotel.ScopeValue) ScopeMessage {
	//: a field copy; the signal already filled its own default name.
	return ScopeMessage{Name: scope.Name, Version: scope.Version}
}
