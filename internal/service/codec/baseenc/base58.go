// Package baseenc — Base58 (Bitcoin alphabet) base-conversion codec.
package baseenc

// base58Alphabet is the Bitcoin Base58 alphabet (the ASCII alphanumerics
// minus the visually ambiguous 0, O, I and l).
const base58Alphabet string = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const (
	// base58Radix is the Base58 alphabet size.
	base58Radix int = 58
	// maxConvBytes caps the input of the O(n²) base-conversion variants
	// (Base58/Base62) at 4 KiB. base-conversion is quadratic, so even a few
	// KiB bounds the work to the low-millisecond range; bulk data belongs in
	// a block codec (base64). Enforced on the raw bytes at Marshal/Append and
	// on the encoded text at Unmarshal.
	maxConvBytes int = 4 * 1024
)

// base58Reverse maps an ASCII byte to its Base58 index, or base45NotMember
// when the byte is absent from the alphabet.
var base58Reverse = buildReverseTable(base58Alphabet)
