// Package crypto — the digital-signature port implemented by each scheme.
package crypto

// Signer is a detached digital-signature scheme over a public/private keypair.
// Keys and signatures are scheme-specific byte slices (their lengths are fixed
// per scheme); the registry dispatches on the Algorithm name. Implementations
// MUST be safe for concurrent use, MUST NOT panic on a malformed key (return
// SigningFailed instead), and Verify MUST run in constant time with respect to
// the signature so it cannot become a timing oracle.
//
// Unlike the AEAD port, a signature scheme has no hidden nonce and no wire
// framing: the caller holds the keypair and chooses what bytes to sign. The
// private key is secret material — treat it like Key.Bytes(): never log it.
//
// IFACE-PLUGIN: the registry hands plug-in Signer instances back to callers
// behind this interface; concrete scheme types stay unexported in their own
// packages.
type Signer interface {
	// Algorithm reports the canonical key under which this scheme registers.
	Algorithm() Algorithm
	// GenerateKey draws a fresh keypair from crypto/rand, returning the public
	// and private key bytes (or KeyGenerationFailed on a host entropy fault).
	GenerateKey() (pub, priv []byte, err error)
	// Sign produces a detached signature over message using priv, or
	// SigningFailed when priv is not a well-formed private key for this scheme.
	Sign(priv, message []byte) (sig []byte, err error)
	// Verify reports whether sig is a valid signature for message under pub. A
	// malformed key or signature is reported as false, never as a panic.
	Verify(pub, message, sig []byte) (ok bool)
}
