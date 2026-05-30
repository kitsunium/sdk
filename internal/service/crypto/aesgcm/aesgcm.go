// Package aesgcm registers the "aes-256-gcm" AEAD scheme (ADR 0013). Importing
// the package (typically a blank import via pkg/v1/crypto) self-registers the
// scheme so crypto.Seal / crypto.Open resolve. It is stdlib-only
// (crypto/aes + crypto/cipher + crypto/rand), so it pulls zero non-stdlib deps
// and keeps pkg/v1/crypto consumers dep-light.
package aesgcm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// algorithm is the canonical registry key for AES-256-GCM.
	algorithm corecrypto.Algorithm = "aes-256-gcm"
	// algID is the frozen 1-byte wire identifier embedded in each box header.
	algID byte = 0x01
	// nonceLen is GCM's standard 96-bit nonce; box layout is
	// [Version][algID][12B nonce][ciphertext||tag].
	nonceLen int = 12
	// headerLen is the fixed [Version][algID] prefix length.
	headerLen int = 2
	// gcmTagLen is the GCM authentication tag length appended by Seal.
	gcmTagLen int = 16
)

// AEAD is the registered AES-256-GCM scheme singleton (no init(); package-level
// var initialiser, mirroring the codec convention).
var AEAD = corecrypto.Register(aesGCM{})

// aesGCM implements core/crypto.AEAD over crypto/cipher's GCM mode.
type aesGCM struct{}

// Algorithm reports the canonical algorithm key.
func (aesGCM) Algorithm() corecrypto.Algorithm {
	//: the literal key consumers pass to SealAs.
	return algorithm
}

// ID reports the frozen wire identifier used for Open dispatch.
func (aesGCM) ID() byte {
	//: byte 1 of every box this scheme produces.
	return algID
}

// Seal encrypts plaintext under key, binding aad, and returns the self-framed
// box with a fresh random nonce. The only realistic failure is a host entropy
// fault from crypto/rand.
func (aesGCM) Seal(key corecrypto.Key, plaintext, aad []byte) (box []byte, err error) {
	//: build the GCM mode from the 256-bit key (rejects a bad key length).
	gcm, gerr := newGCM(key.Bytes())
	//: a refused key aborts before any nonce work.
	if gerr != nil {
		//: surface it as the typed InvalidKey.
		return nil, gerr
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
			Private: "service/crypto/aesgcm.Seal: crypto/rand.Read failed while generating a nonce",
		})
	}
	//: pre-size the box: header + nonce + ciphertext + GCM tag.
	box = make([]byte, 0, len(plaintext)+nonceLen+gcmTagLen+headerLen)
	//: write the self-describing header so Open needs no algorithm argument.
	box = append(box, corecrypto.Version, algID)
	//: the nonce travels in the clear (standard for GCM) ahead of the tag.
	box = append(box, nonce[:]...)
	//: gcm.Seal appends ciphertext||tag onto the header+nonce prefix.
	return gcm.Seal(box, nonce[:], plaintext, aad), nil
}

// Open validates the framing, decrypts, and verifies aad. Every failure returns
// the shared non-oracle DecryptionFailed so this scheme cannot become an oracle.
func (aesGCM) Open(key corecrypto.Key, box, aad []byte) (plaintext []byte, err error) {
	//: a box must carry the header + full nonce before any decryption.
	if len(box) < headerLen+nonceLen || box[0] != corecrypto.Version || box[1] != algID {
		//: malformed or wrong-version framing — non-oracle failure.
		return nil, corecrypto.DecryptionFailed
	}
	//: rebuild the GCM mode; a refused key collapses to the non-oracle error.
	gcm, gerr := newGCM(key.Bytes())
	//: a key-length fault must not be distinguishable during Open.
	if gerr != nil {
		//: never reveal it — return the undifferentiated sentinel.
		return nil, corecrypto.DecryptionFailed
	}
	//: split the in-clear nonce from the ciphertext||tag remainder.
	nonce := box[headerLen : headerLen+nonceLen]
	ciphertext := box[headerLen+nonceLen:]
	//: gcm.Open verifies the tag + aad and returns the plaintext or an error.
	pt, oerr := gcm.Open(nil, nonce, ciphertext, aad)
	//: a failed tag/aad check is indistinguishable from any other failure.
	if oerr != nil {
		//: collapse to the single non-oracle sentinel.
		return nil, corecrypto.DecryptionFailed
	}
	//: authenticated plaintext.
	return pt, nil
}

// newGCM builds the GCM cipher.AEAD from the raw key bytes. A key the cipher
// rejects (it never should — Key is always KeyLen bytes) surfaces as the typed
// InvalidKey. Takes []byte (not the Key) so the helper stays minimal.
func newGCM(keyBytes []byte) (gcm cipher.AEAD, err error) {
	//: 32-byte key selects AES-256.
	block, berr := aes.NewCipher(keyBytes)
	//: a wrong-size key is rejected by the cipher constructor.
	if berr != nil {
		//: surface it as the typed data error, never a panic.
		return nil, corecrypto.InvalidKey
	}
	//: standard 96-bit-nonce GCM over the AES block.
	mode, gerr := cipher.NewGCM(block)
	//: NewGCM only fails on an exotic block size (defensive).
	if gerr != nil {
		//: same typed data error for the unreachable path.
		return nil, corecrypto.InvalidKey
	}
	//: ready-to-use authenticated cipher.
	return mode, nil
}
