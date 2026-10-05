package otel

// ScopeValue identifies the INSTRUMENTATION that produced the telemetry — the
// library, package or module that minted the instruments or started the spans,
// as opposed to the service that runs it. OpenTelemetry calls it
// InstrumentationScope, and it is what lets a backend tell "the SDK's own http
// metrics" apart from "the application's".
//
// SchemaURL and scope-level attributes are absent for the reason
// ResourceValue's SchemaURL is: both are optional in the specification, nothing
// in this SDK produces either, and an always-empty field is a placeholder.
type ScopeValue struct {
	// Name is the instrumenting library's name. Empty means unset and
	// resolves to the signal's own default — see NormalizeScope.
	Name string
	// Version is the instrumenting library's version. It is optional in the
	// specification and stays empty when the caller has none — inventing one
	// would be a claim about code this package cannot see.
	Version string
}

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
