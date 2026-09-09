// Package token — the HMAC-SHA-256 binding.
package token

import (
	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
	coretoken "github.com/kitsunium/sdk/internal/core/token"
	"github.com/kitsunium/sdk/internal/service/crypto/hmacsha2"
)

// hs256Binding is HMAC-SHA-256 over a 256-bit shared secret. It is the only
// binding that is BOTH halves, because a symmetric key that can verify can
// also mint — which is the property a caller should have to notice.
type hs256Binding struct {
	// secret is the redacting core key type. It is a struct with an
	// unexported field, so an *ecdsa.PublicKey cannot become one by
	// conversion — that is the compile-time half of the confusion defence.
	secret corecrypto.Key
}

// algorithm reports HS256.
func (hs256Binding) algorithm() coretoken.Algorithm {
	//: fixed at the type, not read from anything.
	return coretoken.AlgorithmHS256
}

// sign returns the HMAC-SHA-256 tag over input.
func (b hs256Binding) sign(input []byte) (sig []byte, err error) {
	//: the SDK's registered scheme singleton — no registry lookup, so there
	//: is no "algorithm not imported" failure mode on the signing path.
	return hmacsha2.MAC.Tag(b.secret, input), nil
}

// verify recomputes the tag and compares it in constant time.
func (b hs256Binding) verify(input, sig []byte) bool {
	//: hmacsha2 compares with hmac.Equal, so this is never a timing oracle.
	//: it is the ONLY comparison in this package that touches a secret, which
	//: is why nothing here ever reaches for bytes.Equal.
	return hmacsha2.MAC.Verify(b.secret, input, sig)
}
