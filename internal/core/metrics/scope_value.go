// Package metrics — InstrumentationScope: what instrumented the telemetry.
package metrics

// DefaultScopeName names this SDK as the instrumenting library when a caller
// declares no scope of their own. An empty scope name is legal in the
// specification and useless in a backend, so the meter fills in the one thing
// it can state truthfully: which library minted the instruments.
const DefaultScopeName string = "github.com/kitsunium/sdk/pkg/v1/metrics"

// ScopeValue identifies the INSTRUMENTATION that produced the metrics — the
// library, package or module that minted the instruments, as opposed to the
// service that runs it. OpenTelemetry calls it InstrumentationScope, and it is
// what lets a backend tell "the SDK's own http metrics" apart from "the
// application's".
//
// SchemaURL and scope-level attributes are absent for the reason
// ResourceValue's SchemaURL is: both are optional in the specification, nothing
// in this SDK produces either, and an always-empty field is a placeholder.
type ScopeValue struct {
	// Name is the instrumenting library's name. Empty means unset and
	// resolves to DefaultScopeName.
	Name string
	// Version is the instrumenting library's version. It is optional in the
	// specification and stays empty when the caller has none — inventing one
	// would be a claim about code this package cannot see.
	Version string
}

// Normalized returns the ScopeValue a Meter publishes, filling an empty Name
// with DefaultScopeName. Version is left exactly as supplied.
func (s ScopeValue) Normalized() ScopeValue {
	//: a named scope is the caller's and is never overwritten.
	if s.Name != "" {
		//: honoured as written.
		return s
	}
	//: the only truthful default: the library that minted the instruments.
	return ScopeValue{Name: DefaultScopeName, Version: s.Version}
}
