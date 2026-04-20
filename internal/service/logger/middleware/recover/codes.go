// Package recover: codes.go — range 5200-5299 reserved for the recover
// Sink. Codes are declared at source as typed constants; the errs registry
// audit verifies uniqueness and range membership.
package recover

// range: 5200-5299

// CodeRecoverPanicked identifies a Write call where the wrapped downstream
// sink panicked. The sentinel wraps a fmt.Errorf-style string of the panic
// value so callers can treat panics as ordinary errors.
const CodeRecoverPanicked int = 5201

// CodeRecoverDownstreamNil identifies a New call made with a nil downstream.
const CodeRecoverDownstreamNil int = 5202
