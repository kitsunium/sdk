package baseenc

// variantSpec is the per-variant table row that drives Name / MIMETypes /
// Extensions. Hoisted into a package-level array so each method call is a
// pointer dereference rather than a string allocation.
type variantSpec struct {
	name       string
	mimeTypes  []string
	extensions []string
}

// variantSpecs is the source-of-truth table. Index equals the variant
// constant value; new variants append at the end so existing slots never
// shift (matches the iota stability rule already documented for the
// legacy pkg/v1/data/codec/baseenc Encoding enum).
var variantSpecs = [...]variantSpec{
	//: variantBase64 — standard base64.
	{
		name:       "base64",
		mimeTypes:  []string{"application/base64", "text/base64"},
		extensions: []string{".b64", ".base64"},
	},
	//: variantBase64URL — URL-safe base64. Only the distinct "application/base64url"
	//: media type is registered: the former "application/base64;url=true" alias
	//: differed from base64's "application/base64" by a parameter alone, which
	//: LookupMIME strips before indexing — so it was unreachable at lookup AND
	//: collided with base64 once registration normalises params symmetrically
	//: (issue #36). base64url stays routable by name and the .b64url extension.
	{
		name:       "base64url",
		mimeTypes:  []string{"application/base64url"},
		extensions: []string{".b64url"},
	},
	//: variantBase32 — standard base32.
	{
		name:       "base32",
		mimeTypes:  []string{"application/base32"},
		extensions: []string{".b32"},
	},
	//: variantBase16 — uppercase hex.
	{
		name:       "base16",
		mimeTypes:  []string{"application/base16"},
		extensions: []string{".b16"},
	},
	//: variantHex — lowercase hex.
	{
		name:       "hex",
		mimeTypes:  []string{"application/hex"},
		extensions: []string{".hex"},
	},
	//: variantASCII85 — Adobe Ascii85.
	{
		name:       "ascii85",
		mimeTypes:  []string{"application/ascii85"},
		extensions: []string{".a85"},
	},
	//: variantBase45 — RFC 9285 Base45 (QR-code alphanumeric safe).
	{
		name:       "base45",
		mimeTypes:  []string{"application/base45"},
		extensions: []string{".b45"},
	},
	//: variantBase58 — Bitcoin Base58 (base-conversion, short inputs).
	{
		name:       "base58",
		mimeTypes:  []string{"application/base58"},
		extensions: []string{".b58"},
	},
	//: variantBase62 — Base62 (base-conversion, short inputs).
	{
		name:       "base62",
		mimeTypes:  []string{"application/base62"},
		extensions: []string{".b62"},
	},
}

// newVariant returns the codec for v, for a registered singleton's
// initialiser.
//
// Reading variantSpecs here is load-bearing. Register reads Name, MIMETypes
// and Extensions through the codec.Codec interface, and the Go specification
// (Package initialization) records no dependency through an interface method
// call: with a bare &baseencCodec{variant: v} the singletons in codec.go —
// a file presented before this one — would be initialised BEFORE
// variantSpecs, and only the compiler's static initialisation of constant
// composite literals made that work. A build that turns those literals into
// code (garble -literals) left every Name and alias empty at Register time.
// Referencing the table from a function the initialiser calls makes each
// singleton depend on it, so variantSpecs is initialised first whatever the
// file order or the build.
func newVariant(v variant) *baseencCodec {
	//: index the row: the dependency on variantSpecs, and an out-of-range
	//: variant fails here, at import, instead of in the first spec() call.
	_ = variantSpecs[v]
	//: the codec itself stays the one-field value the rest of the package builds.
	return &baseencCodec{variant: v}
}

// spec returns the variantSpec for c.variant. Bounds are guaranteed by
// the var initialisers — Register only stores in-range variants.
func (c *baseencCodec) spec() *variantSpec {
	//: pointer-into-array avoids cloning the spec on every call.
	return &variantSpecs[c.variant]
}
