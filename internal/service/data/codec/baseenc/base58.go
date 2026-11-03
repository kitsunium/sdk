package baseenc

// base58Alphabet is the Bitcoin Base58 alphabet (the ASCII alphanumerics
// minus the visually ambiguous 0, O, I and l).
const base58Alphabet string = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

const (
	// base58Radix is the Base58 alphabet size.
	base58Radix int = 58
	// maxConvBytes caps the RAW (pre-encode) input of the O(n²) base-conversion
	// variants (Base58/Base62) at 64 KiB. base-conversion is quadratic; since the
	// limb arithmetic (#898) 4 KiB encodes in about 2.5 ms and decodes in
	// 0.6 ms, so 64 KiB — a certificate chain, a multibase CID of a block —
	// stays bounded while bulk data still belongs in a block codec (base64).
	// Enforced on the raw bytes at Marshal/Append.
	maxConvBytes int = 64 * 1024
	// maxConvEncodedBytes caps the ENCODED text at Unmarshal. Base58/Base62
	// EXPAND raw→encoded by ~1.37× (log(256)/log(58|62)), so a maxConvBytes raw
	// input encodes to ~88 KiB; capping the encoded side at the raw cap would
	// make a freshly-Marshalled value un-Unmarshalable. 2× covers the expansion
	// with margin while still bounding the quadratic decode work.
	maxConvEncodedBytes int = 2 * maxConvBytes
)

// base58Reverse maps an ASCII byte to its Base58 index, or base45NotMember
// when the byte is absent from the alphabet.
var base58Reverse = buildReverseTable(base58Alphabet)
