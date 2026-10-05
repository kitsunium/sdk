package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// ScopeOf renders the shared ScopeValue a Meter or a Tracer publishes.
func ScopeOf(scope coreotel.ScopeValue) ScopeMessage {
	//: a field copy; the signal already filled its own default name.
	return ScopeMessage{Name: scope.Name, Version: scope.Version}
}
