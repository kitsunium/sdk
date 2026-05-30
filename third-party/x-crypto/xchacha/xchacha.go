// Package xchacha registers the "xchacha20poly1305" AEAD scheme (ADR 0013).
// Blank-importing the package self-registers the scheme so
// crypto.SealAs("xchacha20poly1305", …) / crypto.Open resolve. It is the ONLY
// place golang.org/x/crypto enters a build for this scheme — declared in the
// root umbrella go.mod under third-party/x-crypto, which no other module
// requires, so pkg/v1 consumers stay dep-light (zero x/crypto) unless they
// opt in with this blank import.
//
// XChaCha20-Poly1305 uses a 192-bit (24-byte) random nonce, eliminating the
// birthday-bound message-count limit that AES-256-GCM's 96-bit random nonce
// imposes — preferred for high-volume random-nonce workloads.
package xchacha

import (
	"crypto/cipher"
	"crypto/rand"

	"golang.org/x/crypto/chacha20poly1305"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// algorithm is the canonical registry key for XChaCha20-Poly1305.
	algorithm corecrypto.Algorithm = "xchacha20poly1305"
	// algID is the frozen 1-byte wire identifier embedded in each box header.
	algID byte = 0x02
	// nonceLen is XChaCha20's 192-bit nonce; box layout is
	// [Version][algID][24B nonce][ciphertext||tag].
	nonceLen int = chacha20poly1305.NonceSizeX
	// headerLen is the fixed [Version][algID] prefix length.
	headerLen int = 2
	// tagLen is the Poly1305 authentication tag length appended by Seal.
	tagLen int = chacha20poly1305.Overhead
)

// AEAD is the registered XChaCha20-Poly1305 scheme singleton (no init();
// package-level var initialiser, mirroring the codec convention).
var AEAD = corecrypto.Register(xChaCha{})

// xChaCha implements core/crypto.AEAD over x/crypto's XChaCha20-Poly1305.
type xChaCha struct{}

// Algorithm reports the canonical algorithm key.
func (xChaCha) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to SealAs.
	return algorithm
}

// ID reports the frozen wire identifier used for Open dispatch.
func (xChaCha) ID() byte {
	//: byte 1 of every box this scheme produces.
	return algID
}

// Seal encrypts plaintext under key, binding aad, and returns the self-framed
// box with a fresh random 192-bit nonce. The only realistic failure is a host
// entropy fault from crypto/rand.
func (xChaCha) Seal(key corecrypto.Key, plaintext, aad []byte) (box []byte, err error) {
	//: build the AEAD from the 256-bit key (rejects a bad key length).
	aead, aerr := newAEAD(key.Bytes())
	//: a refused key aborts before any nonce work.
	if aerr != nil {
		//: surface it as the typed InvalidKey.
		return nil, aerr
	}
	//: a fixed-size buffer for the fresh random nonce — one per Seal, never reused.
	var nonce [nonceLen]byte
	//: fill it from crypto/rand; a fault here is a host entropy problem.
	if _, rerr := rand.Read(nonce[:]); rerr != nil {
		//: wrap the cause as the typed EntropyFailed sentinel.
		return nil, errs.Wrap(rerr, errs.WrapParams{
			Code:    corecrypto.CodeEntropyFailed,
			Reason:  "ENTROPY_FAILED",
			Public:  "Could not gather entropy for encryption",
			Private: "third-party/x-crypto/xchacha.Seal: crypto/rand.Read failed while generating a nonce",
		})
	}
	//: pre-size the box: header + nonce + ciphertext + Poly1305 tag.
	box = make([]byte, 0, len(plaintext)+nonceLen+tagLen+headerLen)
	//: write the self-describing header so Open needs no algorithm argument.
	box = append(box, corecrypto.Version, algID)
	//: the nonce travels in the clear ahead of the tag.
	box = append(box, nonce[:]...)
	//: aead.Seal appends ciphertext||tag onto the header+nonce prefix.
	return aead.Seal(box, nonce[:], plaintext, aad), nil
}

// Open validates the framing, decrypts, and verifies aad. Every failure returns
// the shared non-oracle DecryptionFailed so this scheme cannot become an oracle.
func (xChaCha) Open(key corecrypto.Key, box, aad []byte) (plaintext []byte, err error) {
	//: a box must carry the header + full nonce before any decryption.
	if len(box) < headerLen+nonceLen || box[0] != corecrypto.Version || box[1] != algID {
		//: malformed or wrong-version framing — non-oracle failure.
		return nil, corecrypto.DecryptionFailed
	}
	//: rebuild the AEAD; a refused key collapses to the non-oracle error.
	aead, aerr := newAEAD(key.Bytes())
	//: a key-length fault must not be distinguishable during Open.
	if aerr != nil {
		//: never reveal it — return the undifferentiated sentinel.
		return nil, corecrypto.DecryptionFailed
	}
	//: split the in-clear nonce from the ciphertext||tag remainder.
	nonce := box[headerLen : headerLen+nonceLen]
	ciphertext := box[headerLen+nonceLen:]
	//: aead.Open verifies the tag + aad and returns the plaintext or an error.
	pt, oerr := aead.Open(nil, nonce, ciphertext, aad)
	//: a failed tag/aad check is indistinguishable from any other failure.
	if oerr != nil {
		//: collapse to the single non-oracle sentinel.
		return nil, corecrypto.DecryptionFailed
	}
	//: authenticated plaintext.
	return pt, nil
}

// newAEAD builds the XChaCha20-Poly1305 cipher.AEAD from the raw key bytes. A
// key the constructor rejects (it never should — Key is always KeyLen bytes)
// surfaces as the typed InvalidKey. Takes []byte (not the Key) so the helper
// stays minimal.
func newAEAD(keyBytes []byte) (aead cipher.AEAD, err error) {
	//: NewX selects the 24-byte-nonce XChaCha20-Poly1305 variant.
	mode, aerr := chacha20poly1305.NewX(keyBytes)
	//: a wrong-size key is a typed data error, never a panic.
	if aerr != nil {
		//: surface it as the typed data error.
		return nil, corecrypto.InvalidKey
	}
	//: ready-to-use authenticated cipher.
	return mode, nil
}
