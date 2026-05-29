// Package crypto — the opaque, redacting symmetric Key value type.
package crypto

import "bytes"

// KeyLen is the required symmetric key length in bytes (256-bit). Both shipped
// AEADs — AES-256-GCM and XChaCha20-Poly1305 — take a 32-byte key, so a single
// fixed length keeps the surface tiny and the validation total.
const KeyLen int = 32

// Key is an opaque, redacting 256-bit symmetric key. Its String / GoString
// output is always "<redacted>" so an accidental %v / %s / %#v never leaks the
// secret; Bytes hands a fresh copy to the cipher and is the only way out. All
// copies of a Key share one backing array, so Zeroize on any copy clears the
// secret everywhere — call it when you are done with the key.
type Key struct {
	// raw holds the symmetric key bytes behind a slice so a value-receiver
	// Zeroize can clear the shared backing array; NewKey is the only producer.
	raw []byte
}

// NewKey builds a Key from raw, which MUST be exactly KeyLen (32) bytes. A wrong
// length returns InvalidKey rather than silently truncating or padding; the
// bytes are copied so a later mutation of raw cannot affect the Key.
func NewKey(raw []byte) (key Key, err error) {
	//: reject any non-32-byte input — never truncate or pad a key silently.
	if len(raw) != KeyLen {
		//: surface the typed sentinel; callers HasCode(err, CodeInvalidKey).
		return Key{}, InvalidKey
	}
	//: defensive copy so the caller cannot mutate the key after construction.
	return Key{raw: bytes.Clone(raw)}, nil
}

// Bytes returns a fresh copy of the raw key material for handoff to a cipher.
// Callers MUST NOT log the result; the redaction only covers String / GoString.
// A zero-value Key returns nil.
func (k Key) Bytes() []byte {
	//: clone so the caller cannot mutate the Key's shared backing array.
	return bytes.Clone(k.raw)
}

// Zeroize overwrites the key material with zeros. Because copies share one
// backing array, this clears the secret for every copy of the Key.
func (k Key) Zeroize() {
	//: clear the shared backing array in place (no-op on a zero-value Key).
	clear(k.raw)
}

// String implements fmt.Stringer and always redacts so the key never reaches a
// log line through %v / %s.
func (k Key) String() string {
	//: constant redaction marker regardless of the key contents.
	return "<redacted>"
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump the raw slice.
func (k Key) GoString() string {
	//: same constant marker — %#v must never expose key material.
	return "<redacted>"
}
