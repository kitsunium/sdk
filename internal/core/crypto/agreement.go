// Package crypto — the Agreement port: DH-style key agreement.
package crypto

// Agreement is a key-agreement scheme (Diffie-Hellman style): it produces an
// ephemeral keypair and derives a shared secret from a local private key and a
// peer public key. The raw Shared output is low-level key material and MUST be
// run through a KDF (a Deriver) before use as a key — the pkg/v1 facade never
// hands the raw secret back to generic callers. Implementations self-register
// via RegisterAgreement.
//
// IFACE-PLUGIN: the registry hands plug-in Agreement instances back behind this
// interface; concrete scheme types stay unexported in their own packages.
type Agreement interface {
	// Algorithm reports the canonical key under which this scheme registers.
	Algorithm() Algorithm
	// GenerateKey draws a fresh public/private keypair from the scheme's
	// entropy source.
	GenerateKey() (pub, priv []byte, err error)
	// Shared derives the raw shared secret from priv and peerPub. The result
	// MUST be passed through a KDF before any use as a key.
	Shared(priv, peerPub []byte) (secret []byte, err error)
}
