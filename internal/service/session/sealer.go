// Package session — the AEAD sealer that renders an identifier as a cookie
// value.
package session

import (
	"encoding/base64"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coresession "github.com/kitsunium/sdk/internal/core/session"
)

// cookieAAD prefixes the additional authenticated data of every sealed
// identifier. The caller's purpose is appended to it, so a value minted for one
// purpose does not open under another even though both use the same key.
const cookieAAD string = "kitsunium/sdk/session/cookie/v1|"

// sealedEncoding renders and reads a sealed value: unpadded base64url, decoded
// STRICTLY. Unless a box is a multiple of three bytes long — today's is not —
// the last character of its encoding carries bits no byte uses; a lenient
// decoder ignores them and several cookie strings would open to one session,
// and the AEAD cannot object, because the bytes it authenticates are
// identical. Built once, because Strict returns a new copy of the encoding on
// every call.
var sealedEncoding = base64.RawURLEncoding.Strict()

// sealer seals an identifier under one key and one purpose, both bound at
// construction and neither readable from the sealed value.
//
// It composes the crypto domain rather than reimplementing it: core/crypto.Seal
// resolves the AEAD, generates the nonce inside Seal so it never reaches a
// caller, and emits a self-describing box. This type adds the domain
// separation and the base64url rendering, and nothing else.
type sealer struct {
	// key is the AEAD key. Redacting: its String is "<redacted>".
	key corecrypto.Key
	// aad is cookieAAD + the caller's purpose, computed once.
	aad []byte
}

// NewSealer returns a [coresession.Sealer] binding key and purpose.
//
// purpose is REQUIRED. It is what keeps two things sealed under one key apart —
// a session cookie and a CSRF token, say, or the same application's staging and
// production cookies — and an empty one is refused rather than read as "no
// separation", which is ADR 0031's inert-policy failure applied to a security
// boundary.
func NewSealer(key corecrypto.Key, purpose string) (seal coresession.Sealer, err error) {
	//: a zero Key yields nil bytes; a wrong-length one is refused the same way.
	if len(key.Bytes()) != corecrypto.KeyLen {
		//: the key material itself is never named in the error.
		return nil, wrapAs(coresession.InvalidConfig, nil, kerrs.String("field", "Key"))
	}
	//: no default purpose exists that would mean anything.
	if purpose == "" {
		//: InvalidPurpose.
		return nil, wrapAs(InvalidPurpose, nil)
	}
	//: the AAD is fixed for the sealer's lifetime; nothing reads it back.
	return sealer{key: key, aad: []byte(cookieAAD + purpose)}, nil
}

// Seal renders id as an opaque string safe to use verbatim as a cookie value.
//
// The output is unpadded base64url over the crypto domain's self-describing box
// ([Version][alg-id][nonce][ciphertext||tag]), so every character is in RFC
// 6265's cookie-octet set and no further escaping is needed. Nothing but the
// identifier goes inside: a sealed value carrying its own expiry would be a
// token, and would inherit a token's revocation problem.
func (s sealer) Seal(id coresession.ID) (sealed string, err error) {
	//: the zero identifier names nothing and is not sealed.
	if id.IsZero() {
		//: InvalidID.
		return "", coresession.InvalidID
	}
	//: Reveal is the one exit from ID, and this is one of its two legitimate
	//: destinations — the other being a Set-Cookie value the framework writes.
	box, sealErr := corecrypto.Seal(sealAlgorithm, s.key, []byte(id.Reveal()), s.aad)
	//: a seal failure means the AEAD or the key is unusable.
	if sealErr != nil {
		//: the cause travels as a field, never as the origin.
		return "", wrapAs(coresession.SealInvalid, sealErr)
	}
	//: cookie-safe rendering, no padding to be stripped by a proxy, and the
	//: unused bits clear — the one spelling Open accepts.
	return sealedEncoding.EncodeToString(box), nil
}

// Open reverses [sealer.Seal].
//
// Every failure returns the same [coresession.SealInvalid] verdict and a zero
// ID: a value that is not base64, a value that is base64 but not a box, a box
// under the wrong key, a box under the right key and the wrong purpose, and a
// box that opens to something that is not an identifier. Telling them apart
// would let a forger binary-search a value one property at a time — the same
// reasoning that makes crypto.Open non-oracle, carried one layer up rather than
// undone here.
func (s sealer) Open(sealed string) (id coresession.ID, err error) {
	box, decodeErr := sealedEncoding.DecodeString(sealed)
	//: not even base64, or a second spelling of a real value — same verdict
	//: as a forged tag.
	if decodeErr != nil {
		//: SealInvalid, with no detail.
		return coresession.ID{}, coresession.SealInvalid
	}
	plain, openErr := corecrypto.Open(s.key, box, s.aad)
	//: tampering, truncation, wrong key, wrong purpose.
	if openErr != nil {
		//: SealInvalid, with no detail.
		return coresession.ID{}, coresession.SealInvalid
	}
	parsed, parseErr := coresession.ParseID(string(plain))
	//: authenticated plaintext that is not an identifier means the key is
	//: being used for two different things — still one verdict outward.
	if parseErr != nil {
		//: SealInvalid, with no detail.
		return coresession.ID{}, coresession.SealInvalid
	}
	//: an authenticated identifier. It is still only an identifier: whether it
	//: names a LIVE session is the Store's answer, not the sealer's.
	return parsed, nil
}
