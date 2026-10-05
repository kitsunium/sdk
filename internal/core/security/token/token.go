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
