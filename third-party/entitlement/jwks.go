// Package entitlement - the published keys GitHub signs its OIDC tokens with.
//
// Unlike the roster, this set carries no vendor signature: it is authenticated
// by TLS to a fixed host and nothing else. That is weaker, and it is why a
// token verified against it grants only a free CI seat — never a licence. The
// worst a substituted JWKS can do is hand out seats it should not; it cannot
// make an unlicensed machine licensed, because the device path does not
// consult it at all.
package entitlement

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
)

// ActionsJWKSURL is where GitHub publishes the keys. Fixed, https, and never
// taken from a token: letting the thing being verified choose its own key
// source is the whole trust decision handed to the attacker.
const ActionsJWKSURL string = ActionsIssuer + "/.well-known/jwks"

// maxJWKSKeys caps how many entries a key set may carry. GitHub publishes a
// handful; a set with thousands is either broken or an attempt to make key
// selection expensive.
const maxJWKSKeys int = 32

// maxJWKSBytes caps the document before it is decoded at all.
//
// The entry-count limit below only applies AFTER json.Unmarshal has already
// built every element in memory, so a multi-megabyte key set would be paid for
// in full before being refused. GitHub's own set is a couple of kilobytes.
const maxJWKSBytes int = 1 << 20

// maxRSAModulusBits refuses a modulus larger than any real signing key.
//
// rsa.VerifyPKCS1v15 does modular arithmetic over whatever it is given, so an
// absurdly large modulus turns a verification into a long computation — and
// this runs before anything has authenticated the key set.
const maxRSAModulusBits int = 8192

// JWKValue is one published signing key, in the JWK shape.
//
// Only the fields needed to rebuild an RSA public key and to decide whether it
// may verify a signature are declared. Anything else GitHub publishes is
// ignored on purpose: a field that is not read cannot be relied on by
// accident.
type JWKValue struct {
	// KeyType must be RSA for anything this package can verify.
	KeyType string `json:"kty"`
	// KeyID is what a token's `kid` header selects.
	KeyID string `json:"kid"`
	// Use, when present, must be "sig": a key published for encryption is
	// being repurposed if it verifies signatures.
	Use string `json:"use"`
	// Algorithm, when present, must be RS256.
	Algorithm string `json:"alg"`
	// Modulus is the RSA modulus, base64url, minimally encoded.
	Modulus string `json:"n"`
	// Exponent is the RSA public exponent, base64url, minimally encoded.
	Exponent string `json:"e"`
}

// JWKSValue is a published key set.
type JWKSValue struct {
	// Keys are the published signing keys.
	Keys []JWKValue `json:"keys"`
}

// decodeJWKS decodes a published key set, refusing what is not one before any
// of it is trusted.
//
// Size is checked before json.Unmarshal because the entry-count limit only
// applies once the whole document has already been built in memory, which is
// too late to be a limit at all.
func decodeJWKS(raw []byte) (decoded JWKSValue, err error) {
	//: Refuse by size first: everything below costs memory proportional to
	//: what an untrusted endpoint chose to send.
	if len(raw) > maxJWKSBytes {
		//: Refuse the oversized document.
		return decoded, fmt.Errorf("%w: key set larger than %d bytes", ErrCIUnverifiable, maxJWKSBytes)
	}
	//: A key set we cannot decode tells us nothing.
	if unmarshalErr := json.Unmarshal(raw, &decoded); unmarshalErr != nil {
		//: Report it as unverifiable rather than proceed with no keys, which
		//: would read as "unknown kid" and send the caller refreshing forever.
		return decoded, fmt.Errorf("%w: malformed key set: %w", ErrCIUnverifiable, unmarshalErr)
	}
	//: An oversized set is not one we should spend time selecting from.
	if len(decoded.Keys) > maxJWKSKeys {
		//: Refuse the oversized set.
		return decoded, fmt.Errorf("%w: key set carries %d keys", ErrCIUnverifiable, len(decoded.Keys))
	}
	//: A document shaped like a key set.
	return decoded, nil
}

// ParseJWKS decodes a key set and builds the usable RSA keys from it.
//
// Unusable entries are skipped rather than fatal: GitHub may publish a key
// type or use this package does not verify, and refusing the whole set over
// one of them would take down every CI seat for a reason unrelated to any of
// them. A set with NO usable key is an error, because that is
// indistinguishable from having no keys at all.
func ParseJWKS(raw []byte) (keys map[string]*rsa.PublicKey, err error) {
	decoded, decodeErr := decodeJWKS(raw)
	//: A document that is not a key set cannot yield keys.
	if decodeErr != nil {
		//: Propagate the decode failure.
		return nil, decodeErr
	}

	built := make(map[string]*rsa.PublicKey, len(decoded.Keys))
	//: Every kid encountered, usable or not, so the duplicate check does not
	//: depend on the order entries happen to appear in.
	seen := make(map[string]bool, len(decoded.Keys))
	//: Build every entry we can use, and ignore the rest.
	for _, entry := range decoded.Keys {
		//: An entry with no kid cannot be selected by a token, so it could
		//: only ever be reached by trying every key — which is exactly what
		//: the kid requirement exists to prevent.
		if entry.KeyID == "" {
			continue
		}
		//: Two entries claiming one kid make selection ambiguous, and an
		//: ambiguous trust anchor is not one.
		if seen[entry.KeyID] {
			//: Refuse a set that names a key twice.
			return nil, fmt.Errorf("%w: key set repeats kid %q", ErrCIUnverifiable, entry.KeyID)
		}
		seen[entry.KeyID] = true

		key, keyErr := rsaKeyFromJWK(&entry)
		//: An entry we cannot use is skipped, not fatal: see the doc above.
		if keyErr != nil {
			continue
		}
		built[entry.KeyID] = key
	}
	//: No usable key is indistinguishable from no keys, and silently
	//: returning an empty set would report every token as an unknown kid.
	if len(built) == 0 {
		//: Refuse an unusable set.
		return nil, fmt.Errorf("%w: key set holds no usable RS256 key", ErrCIUnverifiable)
	}
	//: The keys a token's kid can select from.
	return built, nil
}
