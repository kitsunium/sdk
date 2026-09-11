// Package token — the ECDSA P-256 signing binding.
package token

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"

	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// es256Signing is ECDSA P-256 signing with the RFC 7518 §3.4 R||S encoding.
type es256Signing struct {
	// key is the private key; its curve and point are checked at construction.
	key *ecdsa.PrivateKey
}

// algorithm reports ES256.
func (es256Signing) algorithm() coretoken.Algorithm {
	//: fixed at the type.
	return coretoken.AlgorithmES256
}

// sign hashes input with SHA-256 and emits the fixed-width R||S signature.
//
// This is the one algorithm here that does NOT go through the SDK's registered
// signer: service/crypto/ecdsasig speaks ASN.1/DER, which is the right shape
// for X.509 and the wrong shape for JOSE. Transcoding DER to R||S would mean
// re-parsing an attacker-supplied structure on the verify path, so the JOSE
// encoding is produced directly from crypto/ecdsa instead — one primitive, one
// encoding, no converter to get wrong.
func (b es256Signing) sign(input []byte) (sig []byte, err error) {
	digest := sha256.Sum256(input)
	//: crypto/ecdsa draws the nonce from crypto/rand.
	r, s, serr := ecdsa.Sign(rand.Reader, b.key, digest[:])
	//: an entropy fault is the only realistic failure.
	if serr != nil {
		//: the cause IS carried here, unlike the token-parsing paths: an
		//: entropy fault is about this host, not about anything an attacker
		//: sent, so nothing it says can leak a token.
		return nil, errs.Wrap(serr, errs.WrapParams{
			Code:     coretoken.CodeIssueFailed,
			Reason:   "ISSUE_FAILED",
			Public:   "The token could not be issued",
			Private:  "service/token: ecdsa.Sign failed on the P-256 signing key",
			ExitCode: exitConfigFault,
		})
	}
	var fixed [p256SigLen]byte
	//: FillBytes left-pads to the fixed width, which is what §3.4 requires.
	r.FillBytes(fixed[:p256CoordLen])
	s.FillBytes(fixed[p256CoordLen:])
	//: exactly 64 octets, always.
	return fixed[:], nil
}
