// Package token — the algorithm-bound key contracts and their constructors.
//
// This file is where algorithm confusion is made unwritable. Each binding
// holds ONE Go key type and reports ONE algorithm, and the bind* helpers below
// are the only way to build one. There is no `bind(alg, []byte)`, because a
// function that takes an algorithm name and a bag of bytes is a function whose
// caller can be talked into passing a public key where a shared secret goes.
package token

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// p256CoordLen is the fixed octet width of a P-256 field element.
	// RFC 7518 §3.4 requires each half of an ES256 signature to be exactly
	// this wide, left-padded — a "compact" 31-octet R is a different number,
	// not a tidier one.
	p256CoordLen int = 32
	// p256SigLen is the total ES256 signature width: R||S, both fixed.
	p256SigLen int = 2 * p256CoordLen
)

// signingKey is the private half of a binding: one algorithm, one key.
type signingKey interface {
	// algorithm reports what this key signs as.
	algorithm() coretoken.Algorithm
	// sign produces a detached signature over input.
	sign(input []byte) (sig []byte, err error)
}

// verifyingKey is the public half of a binding: one algorithm, one key.
type verifyingKey interface {
	// algorithm reports what this key verifies.
	algorithm() coretoken.Algorithm
	// verify reports whether sig authenticates input. It returns a bool and
	// not an error: "this signature is wrong" is an answer, not a fault.
	verify(input, sig []byte) bool
}

// keyUnsuitable returns the KeyUnsuitable verdict carrying a fixed detail.
//
// It wraps the SENTINEL rather than the underlying cause, and that is not a
// stylistic choice: several of these causes are themselves *errs.Error values
// out of internal/service/crypto/jwk, and Wrap's origin-wins rule would make
// the RESULT carry the jwk package's code (0.3.42.*) instead of this domain's
// KeyUnsuitable. A caller routing on the code would then be reading a verdict
// about a key format where they asked about a token.
//
// detail is a fixed string chosen at the call site — never key material.
func keyUnsuitable(detail string) error {
	//: origin-wins on the sentinel keeps code, reason, public and exit code.
	return errs.Wrap(coretoken.KeyUnsuitable, errs.WrapParams{},
		errs.String("detail", detail))
}

// bindSecret validates a symmetric key and returns the HS256 binding.
//
// core/crypto.Key is fixed at 256 bits, so a short passphrase cannot become
// one — which covers the LENGTH half of RFC 8725 §3.5. The ENTROPY half is not
// something this package can measure: derive the key (pkg/v1/kdf) rather than
// typing one.
func bindSecret(secret corecrypto.Key) (binding hs256Binding, err error) {
	//: the zero Key holds no material; Bytes() would hand back nil.
	if len(secret.Bytes()) != corecrypto.KeyLen {
		//: refuse at construction, never at the first token.
		return hs256Binding{}, keyUnsuitable("HS256 needs a 256-bit crypto.Key")
	}
	//: bound.
	return hs256Binding{secret: secret}, nil
}

// bindP256Public validates an ECDSA public key for ES256.
//
// The point is validated, not just measured: ECDH() rejects an off-curve point
// and the identity, and using an off-curve point is a documented route to key
// recovery (RFC 8725 §3.4).
func bindP256Public(pub *ecdsa.PublicKey) (binding es256Verifying, err error) {
	//: a nil key is a construction-site mistake, and ES256 is P-256 and
	//: nothing else — P-384 has its own JOSE name.
	if pub == nil || pub.Curve != elliptic.P256() {
		//: refuse at construction.
		return es256Verifying{}, keyUnsuitable("ES256 needs a P-256 public key")
	}
	//: ECDH() is the stdlib's on-curve + non-identity check.
	if _, cerr := pub.ECDH(); cerr != nil {
		//: refuse an invalid point rather than hand it to a verifier.
		return es256Verifying{}, keyUnsuitable("P-256 public key is not a valid curve point")
	}
	//: bound.
	return es256Verifying{key: pub}, nil
}

// bindP256Private validates an ECDSA private key for ES256.
func bindP256Private(priv *ecdsa.PrivateKey) (binding es256Signing, err error) {
	//: a nil key is a construction-site mistake, and ES256 is P-256 only.
	if priv == nil || priv.Curve != elliptic.P256() {
		//: refuse at construction.
		return es256Signing{}, keyUnsuitable("ES256 needs a P-256 private key")
	}
	//: validate the public half's point too — a private key whose public half
	//: is off-curve is corrupt, and signing with it leaks.
	if _, cerr := priv.PublicKey.ECDH(); cerr != nil {
		//: refuse.
		return es256Signing{}, keyUnsuitable("P-256 private key's public half is not a valid curve point")
	}
	//: bound.
	return es256Signing{key: priv}, nil
}

// bindEd25519Public validates an Ed25519 public key for alg.
func bindEd25519Public(pub ed25519.PublicKey, alg coretoken.Algorithm) (binding ed25519Verifying, err error) {
	//: a wrong-length key would make ed25519.Verify panic in the stdlib.
	if len(pub) != ed25519.PublicKeySize {
		//: refuse at construction.
		return ed25519Verifying{}, keyUnsuitable("Ed25519 needs a 32-octet public key")
	}
	//: bound.
	return ed25519Verifying{key: pub, alg: alg}, nil
}

// bindEd25519Private validates an Ed25519 private key for alg.
func bindEd25519Private(priv ed25519.PrivateKey, alg coretoken.Algorithm) (binding ed25519Signing, err error) {
	//: a wrong-length key would make ed25519.Sign panic in the stdlib.
	if len(priv) != ed25519.PrivateKeySize {
		//: refuse at construction.
		return ed25519Signing{}, keyUnsuitable("Ed25519 needs a 64-octet private key")
	}
	//: bound.
	return ed25519Signing{key: priv, alg: alg}, nil
}
