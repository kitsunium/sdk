// Package baseenc — Base58 (Bitcoin alphabet) base-conversion codec.
package baseenc

// base58Alphabet is the Bitcoin Base58 alphabet (the ASCII alphanumerics
// minus the visually ambiguous 0, O, I and l).
const base58Alphabet string = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const (
	// base58Radix is the Base58 alphabet size.
	base58Radix int = 58
	// maxConvBytes caps the RAW (pre-encode) input of the O(n²) base-conversion
	// variants (Base58/Base62) at 4 KiB. base-conversion is quadratic, so even a
	// few KiB bounds the work to the low-millisecond range; bulk data belongs in
	// a block codec (base64). Enforced on the raw bytes at Marshal/Append.
	maxConvBytes int = 4 * 1024
	// maxConvEncodedBytes caps the ENCODED text at Unmarshal. Base58/Base62
	// EXPAND raw→encoded by ~1.37× (log(256)/log(58|62)), so a maxConvBytes raw
	// input encodes to ~5.5 KiB; capping the encoded side at the raw cap would
	// make a freshly-Marshalled value un-Unmarshalable. 2× covers the expansion
	// with margin while still bounding the quadratic decode work.
	maxConvEncodedBytes int = 2 * maxConvBytes
)

// base58Reverse maps an ASCII byte to its Base58 index, or base45NotMember
// when the byte is absent from the alphabet.
var base58Reverse = buildReverseTable(base58Alphabet)
