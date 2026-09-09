// Package token — the Ed25519 verification binding.
package token

import (
	"crypto/ed25519"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/service/crypto/ed25519sig"
)

// ed25519Verifying is Ed25519 verification, algorithm-tagged like its signing
// counterpart: the same keypair backs JOSE EdDSA and PASETO v4.public over
// different signing inputs, and the tag is what stops one satisfying the other.
type ed25519Verifying struct {
	// key is the 32-octet stdlib public key.
	key ed25519.PublicKey
	// alg is AlgorithmEdDSA or AlgorithmPasetoV4Public.
	alg coretoken.Algorithm
}

// algorithm reports the bound algorithm.
func (b ed25519Verifying) algorithm() coretoken.Algorithm {
	//: set once at construction, never read from a token.
	return b.alg
}

// verify reports whether sig authenticates input under the bound public key.
func (b ed25519Verifying) verify(input, sig []byte) bool {
	//: ed25519sig guards the key length and never panics on a bad signature.
	return ed25519sig.Signer.Verify(b.key, input, sig)
}
