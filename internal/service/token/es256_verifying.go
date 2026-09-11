// Package token — the ECDSA P-256 verification binding.
package token

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"math/big"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
)

// es256Verifying is ECDSA P-256 verification over the R||S encoding.
type es256Verifying struct {
	// key is the public key; its curve and point are checked at construction.
	key *ecdsa.PublicKey
}

// algorithm reports ES256.
func (es256Verifying) algorithm() coretoken.Algorithm {
	//: fixed at the type.
	return coretoken.AlgorithmES256
}

// verify reports whether sig is a valid ES256 signature over input.
func (b es256Verifying) verify(input, sig []byte) bool {
	//: exactly 64 octets — this length check is what rejects a DER signature
	//: and any re-padded variant, so one signature has one spelling.
	if len(sig) != p256SigLen {
		//: wrong shape is wrong signature.
		return false
	}
	digest := sha256.Sum256(input)
	r := new(big.Int).SetBytes(sig[:p256CoordLen])
	s := new(big.Int).SetBytes(sig[p256CoordLen:])
	//: crypto/ecdsa rejects out-of-range scalars itself.
	return ecdsa.Verify(b.key, digest[:], r, s)
}
