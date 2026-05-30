// Package crypto — the key-derivation port implemented by each KDF scheme.
package crypto

// Deriver is a key-derivation function for KEY SEPARATION: it expands one
// high-entropy secret into independent, purpose-bound subkeys. It is NOT a
// password hash — the input must already be a strong key (an AEAD Key, an HKDF
// PRK, a Diffie-Hellman shared secret), never a human password. Password
// stretching (argon2id) is a separate, deliberately slow port.
//
// salt is optional domain randomness (nil is allowed); info is a context label
// that binds each subkey to its purpose ("aead-key", "mac-key", …) so two
// derivations from the same secret never collide. Implementations MUST be safe
// for concurrent use.
//
// IFACE-PLUGIN: the registry hands plug-in Deriver instances back to callers
// behind this interface; concrete scheme types stay unexported in their own
// packages.
type Deriver interface {
	// Algorithm reports the canonical key under which this scheme registers.
	Algorithm() Algorithm
	// Derive expands secret (with optional salt and the context label info) into
	// a length-byte subkey, or DerivationFailed when length exceeds the scheme's
	// maximum output.
	Derive(secret, salt []byte, info string, length int) (subkey []byte, err error)
}
