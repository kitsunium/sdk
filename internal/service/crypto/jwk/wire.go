// Package jwk — the RFC 7517 JSON wire shapes and the base64url codec every
// member goes through. Kept apart from the value type so nothing in jwk.go has
// an exported-looking JSON surface: keyJSON is the ONLY struct with json tags,
// and it is unexported, so encoding/json can never reach a KeyValue by
// reflection.
package jwk

import (
	"encoding/base64"
	"encoding/json"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// keyJSON is the RFC 7517 JWK object. Field order is the emitted member order,
// so this package's own output is octet-stable; a document parsed from
// elsewhere round-trips SEMANTICALLY, not octet-for-octet, because RFC 7159
// objects are unordered and unrecognised members are dropped (RFC 7517 §4 lets
// a parser ignore them, and re-emitting members we do not understand would
// mean vouching for them).
type keyJSON struct {
	Kty    string   `json:"kty"`
	Crv    string   `json:"crv,omitempty"`
	Kid    string   `json:"kid,omitempty"`
	Use    string   `json:"use,omitempty"`
	Alg    string   `json:"alg,omitempty"`
	KeyOps []string `json:"key_ops,omitempty"`
	X      string   `json:"x,omitempty"`
	Y      string   `json:"y,omitempty"`
	D      string   `json:"d,omitempty"`
	K      string   `json:"k,omitempty"`
}

// setJSON is the RFC 7517 §5 JWK Set envelope, used in both directions: members
// stay as raw JSON so the decode side can run each through Parse and the encode
// side can splice in whichever rendering the caller asked for. A nil Keys covers
// both an absent and a null "keys" member; an empty array does not.
type setJSON struct {
	Keys []json.RawMessage `json:"keys"`
}

// b64Encode renders raw as unpadded base64url, the only encoding RFC 7515 §2
// admits for a JOSE member.
func b64Encode(raw []byte) string {
	//: RawURLEncoding is base64url WITHOUT the '=' padding.
	return base64.RawURLEncoding.EncodeToString(raw)
}

// b64Decode parses one member as unpadded base64url. Padded input and the
// standard '+' / '/' alphabet are REJECTED rather than accommodated: a JOSE
// member has exactly one valid spelling, and accepting a second one would let
// the same key present two different thumbprints.
func b64Decode(member string) (raw []byte, err error) {
	//: strict alphabet, no padding — anything else is not a JOSE member.
	decoded, derr := base64.RawURLEncoding.DecodeString(member)
	//: surface the typed sentinel while keeping the cause for diagnostics; the
	//: cause names an octet OFFSET, never the material.
	if derr != nil {
		//: wrap so errors.Is(err, InvalidEncoding) still matches on Code+Reason.
		return nil, errs.Wrap(derr, errs.WrapParams{
			Code:    CodeJWKInvalidEncoding,
			Reason:  "INVALID_ENCODING",
			Public:  "JWK member is not valid unpadded base64url",
			Private: "service/crypto/jwk: base64.RawURLEncoding rejected a member (padded or wrong alphabet)",
		})
	}
	//: the decoded octets; length checks belong to the caller, which knows the
	//: curve and therefore the mandated size.
	return decoded, nil
}

// b64DecodeFixed parses member and requires exactly want octets. The fixed
// length is the rule RFC 7518 §6.2.1.2 states for EC coordinates and RFC 8037
// §2 for OKP keys: leading zero octets carry meaning, so a "compact" 31-octet
// P-256 coordinate is a different number, not a tidier one.
func b64DecodeFixed(member string, want int) (raw []byte, err error) {
	//: alphabet first — a bad encoding is not a length problem.
	decoded, derr := b64Decode(member)
	//: propagate the already-typed encoding failure untouched.
	if derr != nil {
		//: origin wins; nothing to add here.
		return nil, derr
	}
	//: exact length, never a truncation or a left-pad on our side.
	if len(decoded) != want {
		//: report the sizes, which are public facts about the curve.
		return nil, errs.Wrap(InvalidEncoding, errs.WrapParams{},
			errs.Int("want", want), errs.Int("got", len(decoded)))
	}
	//: a correctly sized field element.
	return decoded, nil
}
