// Package failover: codes.go — range 3900-3999 reserved for the failover
// Sink. Codes are declared at source as typed constants; the errs registry
// audit verifies uniqueness and range membership.
package failover

// range: 3900-3999

// CodeFailoverExhausted identifies a Write call where every downstream
// sink in the failover chain returned a non-nil error. The sentinel wraps
// errors.Join of the per-sink failures so callers can inspect each cause.
const CodeFailoverExhausted int = 3901

// CodeFailoverEmpty identifies a New call made with zero downstream sinks.
const CodeFailoverEmpty int = 3902
