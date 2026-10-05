package kdf

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the stdlib HKDF-SHA256 deriver. Stdlib-only, so importing
	// pkg/v1/crypto/kdf pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/kdf/hkdfsha256"
	"github.com/kitsunium/sdk/internal/service/crypto/kdf/keytree"
)

// HKDFSHA256 is HKDF (RFC 5869) over SHA-256 — extract-and-expand for key
// separation from a strong secret.
const HKDFSHA256 Algorithm = "hkdf-sha256"

// Subkey derives a length-byte subkey from secret using the named scheme. salt
// is optional domain randomness (nil is allowed); info is a context label that
// binds the subkey to its purpose. An unregistered algorithm returns
// UnknownKDFAlgorithm; an over-long length returns DerivationFailed.
func Subkey(a Algorithm, secret, salt []byte, info string, length int) (subkey []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Subkey(corecrypto.Algorithm(a), secret, salt, info, length)
}

// NewKeyTree returns the root KeyTree for master, deriving children under algo.
// The master Key is shared by reference across children — the root owns its
// Zeroize lifetime, so zeroizing master invalidates every derived node.
func NewKeyTree(algo Algorithm, master Key) KeyTree {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return keytree.NewKeyTree(corecrypto.Algorithm(algo), master)
}
