package otlp

import coreotel "github.com/kitsunium/sdk/internal/core/observe/otel"

// ScopeMessage is common.v1.InstrumentationScope: name (1) + version (2). A Meter and
// a Tracer both normalise Name, so it is always present; Version is optional in
// the specification and omitted when the caller has none rather than emitted
// blank.
//
// attributes (3) and droppedAttributesCount (4) are absent: nothing in this SDK
// produces a scope attribute (ADR 0044 §Deferred), and an always-empty field
// is a placeholder (rule 5).
type ScopeMessage struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ScopeOf renders the shared ScopeValue a Meter or a Tracer publishes.
func ScopeOf(scope coreotel.ScopeValue) ScopeMessage {
	//: a field copy; the signal already filled its own default name.
	return ScopeMessage{Name: scope.Name, Version: scope.Version}
}
