package redact

import coreredact "github.com/kitsunium/sdk/internal/core/security/redact"

// The engine satisfies the port it claims (ADR 0160): the build fails before
// any test runs the day a method of either drifts from the other.
var (
	_ coreredact.Redactor = (*Redactor)(nil)
)
