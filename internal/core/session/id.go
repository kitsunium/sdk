// Package session — the opaque, redacting session identifier.
package session

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

// IDLen is the required identifier length in bytes. 32 bytes is 256 bits of
// entropy from crypto/rand: guessing one is not a threat model, it is a
// rounding error. The length is fixed rather than configurable because every
// value a caller could choose below it is a weaker session and every value
// above it only lengthens the cookie.
const IDLen int = 32

// canonicalEncoding is the one spelling an identifier has: unpadded base64url,
// decoded STRICTLY. 32 bytes are 256 bits and 43 characters carry 258, so the
// last character holds two bits no byte uses; a lenient decoder ignores them,
// and four different strings would then name one session. Strict refuses any
// of those bits set. It is built once because Strict returns a new copy of the
// encoding on every call.
var canonicalEncoding = base64.RawURLEncoding.Strict()

// ID is an opaque session identifier. It IS a bearer secret — whoever holds it
// is the session — so this type is built to make leaking it hard:
//
//   - String and GoString render "<redacted>", so an accidental %v, %s or %#v
//     in a log line, an error message or a struct dump prints nothing usable.
//     The public/private split of errs is the same idea applied to errors: a
//     Public string is read by third parties, so no sentinel in this domain
//     ever carries an identifier in one.
//   - [ID.Reveal] is the only way to the canonical string, and it is spelled
//     to read like a mistake at a call site that is not building a cookie.
//   - [ID.Equal] compares in constant time. Comparing identifiers with == on
//     the byte slice would not compile; comparing their Reveal strings would,
//     and would leak the length of the shared prefix.
//   - [ID.Digest] is the value a store indexes and names files by, so the
//     secret itself is never a map key, never a filename, and never at rest.
type ID struct {
	// raw holds exactly IDLen bytes; the zero value holds nil and is not
	// usable. NewID and ParseID are the only producers, and both copy.
	raw []byte
}

// NewID builds an ID from raw, which MUST be exactly [IDLen] bytes. A wrong
// length returns [InvalidID] rather than padding or truncating — a truncated
// identifier is a weaker secret that would still work. The bytes are copied so
// a later mutation of raw cannot reach the ID.
func NewID(raw []byte) (id ID, err error) {
	//: refuse any length but the one; never silently reshape a secret.
	if len(raw) != IDLen {
		//: typed refusal — callers match errs.HasCode(err, CodeInvalidID).
		return ID{}, InvalidID
	}
	//: defensive copy so the caller's buffer is not the ID's storage. A fixed
	//: array rather than a make: the length is a constant of the domain.
	var buf [IDLen]byte
	copy(buf[:], raw)
	//: a usable identifier.
	return ID{raw: buf[:]}, nil
}

// ParseID decodes the canonical form produced by [ID.Reveal] — unpadded
// base64url over exactly [IDLen] bytes. It is the inbound path: what arrives
// from a cookie, a header or a query string reaches the domain through here,
// and anything that is not exactly that shape is refused with [InvalidID]
// before it can be used as a lookup key.
func ParseID(encoded string) (id ID, err error) {
	//: raw (unpadded) URL alphabet — the form Reveal emits and the only form
	//: accepted — decoded strictly, so the unused trailing bits must be zero
	//: and two spellings of one identifier cannot exist.
	raw, decodeErr := canonicalEncoding.DecodeString(encoded)
	//: a malformed identifier is refused, never coerced.
	if decodeErr != nil {
		//: the decoder's own message is dropped: it can echo attacker input.
		return ID{}, InvalidID
	}
	//: length is re-checked by NewID, which also copies.
	return NewID(raw)
}

// Reveal returns the canonical unpadded base64url form — 43 characters, all of
// them in the RFC 6265 cookie-octet set, so the result needs no further
// escaping to be a cookie value.
//
// This is the ONLY way the secret leaves the type. Hand it to a Set-Cookie
// value or to [Sealer.Seal]; never to a logger, a metric label, a URL, or an
// error's Public string. A zero-value ID reveals "".
func (i ID) Reveal() string {
	//: nil raw renders empty rather than panicking; a zero ID names nothing.
	//: The encoder always leaves the unused bits clear, which is exactly the
	//: spelling ParseID's strict decoder accepts.
	return canonicalEncoding.EncodeToString(i.raw)
}

// Digest returns the lowercase hex SHA-256 of the identifier: 64 characters,
// deterministic, and safe to write down.
//
// It is what a [Store] uses as its lookup key and what the file store uses as a
// filename, so the identifier itself is never a map key, never a directory
// entry, and never on disk. A stolen backup therefore yields digests, not
// usable cookies. The digest is a LOOKUP KEY, not an authenticator: it is
// unkeyed, so anyone holding an identifier can compute it — which is exactly
// the point, and exactly why it is not treated as a secret. A zero-value ID
// digests to "".
func (i ID) Digest() string {
	//: a zero ID has no digest; returning the digest of the empty string would
	//: give every zero ID one shared, valid-looking lookup key.
	if len(i.raw) == 0 {
		//: no key at all — a store lookup on "" finds nothing.
		return ""
	}
	sum := sha256.Sum256(i.raw)
	//: hex rather than base64 so the value is filename-safe on every OS,
	//: including the case-insensitive ones.
	return hex.EncodeToString(sum[:])
}

// Equal reports whether i and other are the same identifier, in time that does
// not depend on how many leading bytes they share. Every comparison of a
// session identifier in this SDK goes through here or through crypto/subtle
// directly; none of them uses == or string equality.
func (i ID) Equal(other ID) bool {
	//: ConstantTimeCompare already returns 0 on a length mismatch, so a zero
	//: ID never equals a real one and no early return leaks the length.
	return subtle.ConstantTimeCompare(i.raw, other.raw) == 1
}

// IsZero reports whether the ID names nothing — the zero value, which no
// constructor produces. A store refuses it with [InvalidID].
func (i ID) IsZero() bool {
	//: only the zero value has no bytes; both constructors enforce IDLen.
	return len(i.raw) == 0
}

// String implements fmt.Stringer and always redacts, so an identifier never
// reaches a log line through %v or %s. Use [ID.Reveal] when the destination is
// a cookie, and [ID.Digest] when it is a log line that needs correlation.
func (i ID) String() string {
	//: one constant marker regardless of contents — length would leak too.
	return "<redacted>"
}

// GoString implements fmt.GoStringer so %#v stays redacted: fmt bypasses String
// for Go-syntax formatting and would otherwise dump the backing slice.
func (i ID) GoString() string {
	//: same constant marker as String.
	return "<redacted>"
}
