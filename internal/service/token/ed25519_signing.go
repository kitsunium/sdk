// Package token — the Ed25519 signing binding.
package token

import (
	"crypto/ed25519"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/service/crypto/ed25519sig"
)

// ed25519Signing is Ed25519 signing. It carries its algorithm as a field
// because the same primitive backs two of them — JOSE "EdDSA" and PASETO
// v4.public — over DIFFERENT signing inputs. Reporting which one it is keeps a
// v4.public key from silently satisfying a JWS EdDSA binding.
type ed25519Signing struct {
	// key is the 64-octet stdlib private key.
	key ed25519.PrivateKey
	// alg is AlgorithmEdDSA or AlgorithmPasetoV4Public.
	alg coretoken.Algorithm
}

// algorithm reports the bound algorithm.
func (b ed25519Signing) algorithm() coretoken.Algorithm {
	//: set once at construction, never read from a token.
	return b.alg
}

// sign returns the 64-octet Ed25519 signature over input.
func (b ed25519Signing) sign(input []byte) (sig []byte, err error) {
	//: the SDK's registered scheme singleton; it guards the key length and
	//: returns a typed error instead of ed25519.Sign's panic.
	return ed25519sig.Signer.Sign(b.key, input)
}
