// Package agree is the key-agreement facade: establish a shared symmetric key
// between two parties from their keypairs.
//
// Each party generates a keypair, exchanges public keys, and derives the SAME
// [Key] — without ever transmitting the key itself:
//
//	pubA, privA, _ := agree.GenerateKey(agree.X25519)
//	pubB, privB, _ := agree.GenerateKey(agree.X25519)
//	keyA, _ := agree.SharedKey(agree.X25519, privA, pubB, "app-v1")
//	keyB, _ := agree.SharedKey(agree.X25519, privB, pubA, "app-v1")
//	// keyA and keyB are identical and ready for crypto.Seal.
//
// # The raw Diffie-Hellman secret is never handed back
//
// A raw DH secret is biased key material, unsafe to use directly. [SharedKey]
// runs it through HKDF-SHA256 (bound to the info label) and returns a redacting
// [Key] — the raw secret never leaves the SDK. The info label provides domain
// separation: two applications sharing one keypair derive independent keys.
//
// # Key hygiene
//
// The priv from [GenerateKey] is secret material: hold it like a password, never
// log it, and zero it when done. The returned [Key] redacts in logs; call its
// Zeroize when finished.
//
// # Algorithms
//
// Importing this package activates X25519 (and the HKDF-SHA256 it derives
// through) with zero non-stdlib deps:
//
//   - [X25519] — Diffie-Hellman over Curve25519 (RFC 7748); rejects low-order
//     peer points.
//
// # Stable algorithm strings
//
// The [Algorithm] constants are frozen post-v1.0.0.
package agree
