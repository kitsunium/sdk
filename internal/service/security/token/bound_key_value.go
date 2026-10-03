// Package token — one JWK Set member, with its binding already derived.
package token

// boundKeyValue is one JWK Set member with its verifying binding already
// derived, plus whether that derivation succeeded.
type boundKeyValue struct {
	// binding is the verifying key this member implies. It is meaningful only
	// when usable is true.
	binding verifyingKey
	// usable reports whether bindJWK accepted this member.
	//
	// It is a BOOLEAN and not a nil check on binding, and that is load-bearing:
	// bindJWK's refusals do not all arrive as a nil interface. bindECJWK ends
	// in `return bindP256Public(public)`, whose first result is the VALUE type
	// es256Verifying, so a refusal there reaches this struct as a NON-nil
	// verifyingKey wrapping a zero es256Verifying — whose verify would
	// dereference a nil *ecdsa.PublicKey. bindOctJWK and bindOKPJWK have the
	// same shape through hs256Binding and ed25519Verifying.
	//
	// Those three inner refusals are unreachable today, because jwk validates
	// length, curve and point before any of them can fire — this field is
	// defence against an edit that makes one reachable, not a fix for a panic
	// anything can currently produce. TestABindRefusalIsNotANilInterface pins
	// the shape so the claim stays checkable.
	usable bool
}
