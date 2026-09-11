// Package token declares the security-token domain: the [Issuer] and
// [Verifier] ports, the immutable [ClaimsValue] every concrete format decodes
// into, and the typed verdicts a caller matches on. Concrete formats — JWT/JWS
// compact (RFC 7519) and PASETO v4 — live in internal/service/token; this
// package owns only the contract, so a caller can hold a Verifier without
// knowing which wire format produced it.
//
// # There is no registry, and that is the security decision
//
// Every other pluggable SDK domain resolves an implementation through a
// process-wide registry keyed on a name. A token registry would be keyed on
// the "alg" header — a field the ATTACKER writes. Resolving the verifying
// algorithm from that field is algorithm confusion, the class of bug where an
// RSA/EC public key (public by definition) is fed to HMAC-SHA-256 as a shared
// secret and the forgery verifies. So the algorithm is bound at construction,
// by a constructor that only accepts the ONE key type that algorithm can use,
// and the header is only ever compared against that binding — never consulted
// to choose it. See ADR 0042.
//
// [Algorithm] is a uint8 enum rather than a string for the same reason: the
// unsecured "none" algorithm of RFC 7519 §6 has no representation in this
// type, so no call site can request it and no configuration can enable it. A
// token whose header says "none" is refused with [AlgorithmNone].
package token

// Algorithm is the signature algorithm a token issuer or verifier is bound to.
// The zero value is [AlgorithmUnknown] and is never usable.
//
// The set is closed and deliberately small: an algorithm reaches this enum only
// when the SDK's crypto domain already ships the primitive behind it. "none"
// is absent by construction.
type Algorithm uint8

const (
	// AlgorithmUnknown is the zero value — the algorithm nobody chose. Every
	// constructor refuses it rather than defaulting to one.
	AlgorithmUnknown Algorithm = iota
	// AlgorithmHS256 is JOSE "HS256": HMAC-SHA-256 over a 256-bit shared
	// secret (RFC 7518 §3.2). Symmetric — a verifier can also mint tokens.
	AlgorithmHS256
	// AlgorithmES256 is JOSE "ES256": ECDSA on NIST P-256 with SHA-256 and the
	// fixed-width R||S signature encoding of RFC 7518 §3.4.
	AlgorithmES256
	// AlgorithmEdDSA is JOSE "EdDSA": Ed25519 over the JWS signing input
	// (RFC 8037 §3.1).
	AlgorithmEdDSA
	// AlgorithmPasetoV4Public is PASETO v4.public: Ed25519 over the
	// pre-authentication encoding of the version header, payload and footer.
	// It is a distinct Algorithm from AlgorithmEdDSA even though both use
	// Ed25519, because the bytes that get signed are not the same bytes.
	AlgorithmPasetoV4Public
)

// String reports the wire name of a — the JOSE "alg" header value for the JWS
// algorithms, and the version+purpose header for PASETO. An unknown Algorithm
// renders "unknown", which matches no wire name, so a zero value can never be
// mistaken for a real header on either side of a comparison.
func (a Algorithm) String() string {
	//: closed switch — every constant has exactly one wire spelling.
	switch a {
	//: RFC 7518 §3.2.
	case AlgorithmHS256:
		//: the JOSE registry name.
		return "HS256"
	//: RFC 7518 §3.4.
	case AlgorithmES256:
		//: the JOSE registry name.
		return "ES256"
	//: RFC 8037 §3.1.
	case AlgorithmEdDSA:
		//: the JOSE registry name.
		return "EdDSA"
	//: PASETO's version+purpose plays the role "alg" plays in JOSE — except
	//: that it is not a negotiable field.
	case AlgorithmPasetoV4Public:
		//: the PASETO header, without its trailing separator.
		return "v4.public"
	//: AlgorithmUnknown and any out-of-range value.
	default:
		//: a name no wire format uses, so it never equals a real header.
		return "unknown"
	}
}

// Known reports whether a names an algorithm this domain implements. It is the
// guard every constructor runs before binding a key, so an out-of-range value
// is refused at construction rather than at the first verification.
func (a Algorithm) Known() bool {
	//: the enum is contiguous; anything past the last constant is not ours.
	return a >= AlgorithmHS256 && a <= AlgorithmPasetoV4Public
}

// Issuer mints a signed token carrying claims. Implementations are bound to
// exactly one [Algorithm] and one signing key at construction, MUST be safe for
// concurrent use, and MUST NOT take an algorithm or a key from the claims.
//
// IFACE-PLUGIN: constructors in internal/service/token hand instances back
// behind this interface; the concrete types stay unexported.
type Issuer interface {
	// Issue renders claims as a token string, or returns a typed verdict
	// (IssueFailed / ExpiryRequired / KeyUnsuitable) when it cannot.
	Issue(claims ClaimsValue) (string, error)
}

// Verifier authenticates a token and returns its claims. Implementations are
// bound to exactly one [Algorithm] and one verification key at construction and
// MUST be safe for concurrent use.
//
// The contract has three parts, and all three are security requirements:
//
//  1. The signature is checked BEFORE any claim is read, so a caller never
//     acts on — or logs — the contents of an unauthenticated token.
//  2. The token's own algorithm header is compared against the binding and is
//     never used to select a key, an algorithm, or a code path.
//  3. Failure returns exactly one typed verdict and a zero ClaimsValue. There
//     is no "invalid, but here are the claims anyway" path.
//
// IFACE-PLUGIN: constructors in internal/service/token hand instances back
// behind this interface; the concrete types stay unexported.
type Verifier interface {
	// Verify authenticates token and returns its claims, or one of the typed
	// verdicts declared in this package.
	Verify(token string) (ClaimsValue, error)
}
