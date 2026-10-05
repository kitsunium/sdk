package crypto

import (
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"

	// Activates the default AES-256-GCM scheme. Stdlib-only, so importing
	// pkg/v1/crypto pulls zero non-stdlib dependencies.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aead/aesgcm"

	// Activates the stdlib streaming AES-256-GCM scheme behind SealStream /
	// OpenStream. Stdlib-only, so it preserves the dep-light invariant.
	_ "github.com/kitsunium/sdk/internal/service/crypto/aead/streamaead"
)

// streamAlgorithm is the frozen algorithm key of the stdlib streaming scheme
// SealStream / OpenStream dispatch to (activated by the blank import above).
const streamAlgorithm Algorithm = "aes-256-gcm-stream"

// AESGCM is the default algorithm: AES-256-GCM. Active out of the box.
const AESGCM Algorithm = "aes-256-gcm"

// XChaCha20Poly1305 is the 192-bit-nonce AEAD, preferred for high-volume
// random-nonce workloads. Pass it to SealAs after a blank import of
// third-party/x-crypto/xchacha (which alone pulls golang.org/x/crypto):
//
//	import _ "github.com/kitsunium/sdk/third-party/x-crypto/xchacha"
//	box, _ := crypto.SealAs(crypto.XChaCha20Poly1305, k, pt, aad)
const XChaCha20Poly1305 Algorithm = "xchacha20poly1305"

// defaultAlgorithm is the scheme Seal uses when the caller does not pick one.
const defaultAlgorithm Algorithm = AESGCM

// Algorithm is the stable identifier of an AEAD scheme. Use it with SealAs. It
// is a defined type distinct from the other crypto-family Algorithm types (hash,
// mac, sign, …), so the compiler rejects feeding a hash or signature constant
// into an AEAD call (V104) — the seven registries are separate keyspaces, and
// the type system now enforces that separation the way typed Format/Level
// discipline does elsewhere.
type Algorithm corecrypto.Algorithm

// Seal encrypts plaintext under k using the default algorithm (AES-256-GCM),
// binding aad, and returns a self-describing box. The nonce is generated and
// embedded for you. Pass nil aad when unused.
func Seal(k Key, plaintext, aad []byte) (box []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Seal(corecrypto.Algorithm(defaultAlgorithm), k, plaintext, aad)
}

// SealAs is Seal with an explicit algorithm — e.g. a scheme activated by its own
// blank import. An unregistered algorithm returns UnknownAlgorithm.
func SealAs(a Algorithm, k Key, plaintext, aad []byte) (box []byte, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.Seal(corecrypto.Algorithm(a), k, plaintext, aad)
}

// SealStream wraps dst so writes are sealed under k with aad in fixed 64 KiB
// authenticated chunks. The returned io.WriteCloser buffers and seals chunks as
// they fill; Close writes the final authenticated chunk and MUST be called to
// produce a valid stream. Pass nil aad when unused.
func SealStream(dst io.Writer, k Key, aad []byte) (sealed io.WriteCloser, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.SealStream(corecrypto.Algorithm(streamAlgorithm), k, dst, aad)
}

// OpenStream wraps src so reads are opened under k with aad, decrypting the
// chunked stream produced by SealStream. The returned io.Reader never surfaces a
// chunk's plaintext before it authenticates, returns EOF only after the final
// chunk verifies, and surfaces a truncated stream as StreamTruncated. Pass nil
// aad when unused.
func OpenStream(src io.Reader, k Key, aad []byte) (opened io.Reader, err error) {
	//: convert the domain-typed Algorithm to the core key at the boundary.
	return corecrypto.OpenStream(corecrypto.Algorithm(streamAlgorithm), k, src, aad)
}
