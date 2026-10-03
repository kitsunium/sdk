// Package entitlement — the crypto domain's operations this engine uses: a
// signature check and two digests, asked through pkg/v1 rather than crypto/*
// (ADR 0158 §2). RS256, which the crypto domain has no scheme for, is not here:
// it is oidc.go's, and its doc comment says why it stays local.
package entitlement

import (
	"github.com/kitsunium/sdk/pkg/v1/hash"
	"github.com/kitsunium/sdk/pkg/v1/sign"
)

// sha256Size is the width of a SHA-256 digest, in bytes.
const sha256Size int = 32

// verifiedBy reports whether signature is key's ed25519 signature over message.
//
// It asks the crypto domain's signing facade (ADR 0158 §2) rather than calling
// crypto/ed25519 itself: one verifier for the vendor's roster, the self-update
// manifest and a Roughtime answer, and the one the rest of the SDK uses. The
// facade answers a key or a signature of the wrong length with false rather
// than a panic, and an error only for an algorithm it does not register —
// which Ed25519 always is, since importing the facade registers it. An error
// therefore reads as "not verified": nothing that failed to verify is believed.
func verifiedBy(key, message, signature []byte) bool {
	verified, verifyErr := sign.Verify(sign.Ed25519, key, message, signature)
	//: Verified, and by the scheme the facade was asked for.
	return verifyErr == nil && verified
}

// digestOf returns data's digest under algorithm, through the crypto domain's
// hash facade.
//
// The facade registers every algorithm it names when it is imported — by this
// file, so before any code of this package runs — and the one error it can
// return, an unknown algorithm, is therefore unreachable here. It is not turned
// into a value: both callers COMPARE what this returns, and a comparison
// against a fallback nobody computed is the defect a digest exists to prevent.
// It panics, which is how the framework already says "the SDK registers it"
// (framework/internal/kit's journal chain).
func digestOf(algorithm hash.Algorithm, data []byte) []byte {
	sum, sumErr := hash.Sum(algorithm, data)
	//: Unreachable: see the doc comment.
	if sumErr != nil {
		panic("entitlement: " + string(algorithm) + " is not registered: " + sumErr.Error())
	}
	//: The digest, as the domain computed it.
	return sum
}

// payloadDigest is the SHA-256 of the bytes a roster signature covers: what
// separates two statements the vendor signed at one instant (markRecord).
func payloadDigest(payload []byte) [sha256Size]byte {
	var digest [sha256Size]byte
	copy(digest[:], digestOf(hash.SHA256, payload))
	//: The fixed-width form a markRecord compares with ==.
	return digest
}

// sha512Prefix is the first size bytes of data's SHA-512: a Roughtime Merkle
// node, which the format truncates to roughtimeHashSize.
func sha512Prefix(data []byte, size int) []byte {
	//: Truncated to the width the caller's format uses.
	return digestOf(hash.SHA512, data)[:size]
}
