package crypto

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
)

// redactedKey is the one rendering a Key has, whatever it holds.
const redactedKey string = "<redacted>"

// KeyLen is the required symmetric key length in bytes (256-bit). Both shipped
// AEADs — AES-256-GCM and XChaCha20-Poly1305 — take a 32-byte key, so a single
// fixed length keeps the surface tiny and the validation total.
const KeyLen int = 32

// Key is an opaque, redacting 256-bit symmetric key. Every rendering — String,
// GoString and Format, so %v, %s, %#v, %d and every other verb — writes
// "<redacted>", so an accidental print never leaks the secret; Bytes hands a
// fresh copy to the cipher and is the only way out. All copies of a Key share
// one backing array, so Zeroize on any copy clears the secret everywhere —
// call it when you are done with the key.
//
// The bytes sit behind a pointer rather than in the struct: fmt cannot call
// Format on a Key held in an UNEXPORTED field of another struct, so it prints
// that field's own fields instead, and a pointer prints an address where a
// slice would print the key as a list of byte values. A zero-size field of a
// non-comparable type keeps == from compiling, so no pointer comparison can
// pass for a key comparison.
type Key struct {
	// _ makes the struct non-comparable at zero cost: with the bytes behind a
	// pointer, == would otherwise compile and compare allocations.
	_ [0]func()
	// held points at the symmetric key bytes; nil for the zero Key. Every copy
	// shares it, so a value-receiver Zeroize clears the material for all of
	// them; NewKey is the only producer.
	held *keyBytes
}

// keyBytes is the one allocation a Key owns. It is a separate type only so
// that the field a Key carries is a pointer — see the Key doc comment.
type keyBytes struct {
	// raw is the key material, exactly KeyLen bytes.
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
	return Key{held: &keyBytes{raw: bytes.Clone(raw)}}, nil
}

// Bytes returns a fresh copy of the raw key material for handoff to a cipher.
// Callers MUST NOT log the result; the redaction only covers the renderings.
// A zero-value Key returns nil.
func (k Key) Bytes() []byte {
	//: the zero Key has nothing to hand over.
	if k.held == nil {
		//: nil, as documented.
		return nil
	}
	//: copy so the caller cannot mutate the Key's shared backing array. The
	//: material is always KeyLen bytes, so a fixed-size copy needs no second
	//: nil guard — which keeps Bytes cheap enough to inline at its callers.
	return append(make([]byte, 0, KeyLen), k.held.raw...)
}

// Zeroize overwrites the key material with zeros. Because copies share one
// backing array, this clears the secret for every copy of the Key.
func (k Key) Zeroize() {
	//: a zero-value Key holds nothing to clear.
	if k.held == nil {
		//: no-op, as before.
		return
	}
	//: clear the shared backing array in place.
	clear(k.held.raw)
}

// String implements fmt.Stringer and always redacts so the key never reaches a
// log line through %v / %s.
func (k Key) String() string {
	//: constant redaction marker regardless of the key contents.
	return redactedKey
}

// GoString implements fmt.GoStringer so %#v stays redacted too — fmt bypasses
// String for Go-syntax formatting and would otherwise dump the raw slice.
func (k Key) GoString() string {
	//: same constant marker — %#v must never expose key material.
	return redactedKey
}

// Format implements fmt.Formatter, so EVERY verb writes "<redacted>" — %d
// included, which bypasses String and would otherwise print the struct's
// bytes. %q writes it quoted.
func (k Key) Format(state fmt.State, verb rune) {
	//: %q quotes, so a surrounding quoted rendering stays well formed.
	if verb == 'q' {
		//: the marker, quoted.
		writeKeyRendering(state, strconv.Quote(redactedKey))
		//: nothing else to write.
		return
	}
	//: every other verb writes the bare marker.
	writeKeyRendering(state, redactedKey)
}

// writeKeyRendering writes a rendering into the state fmt handed to Format.
// fmt.Formatter has no error return and fmt discards what a write into its own
// buffer reports, so the error is read here and dropped as a visible decision.
func writeKeyRendering(state fmt.State, text string) {
	//: fmt's buffer is the only destination, and it does not fail.
	if _, err := io.WriteString(state, text); err != nil {
		//: nothing to report to — fmt drops the same error.
		return
	}
}
