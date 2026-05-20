// Package baseenc — per-variant identity table.
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
// legacy pkg/v1/codec/baseenc Encoding enum).
var variantSpecs = [...]variantSpec{
	//: variantBase64 — standard base64.
	{
		name:       "base64",
		mimeTypes:  []string{"application/base64", "text/base64"},
		extensions: []string{".b64", ".base64"},
	},
	//: variantBase64URL — URL-safe base64.
	{
		name:       "base64url",
		mimeTypes:  []string{"application/base64url", "application/base64;url=true"},
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
}

// spec returns the variantSpec for c.variant. Bounds are guaranteed by
// the var initialisers — Register only stores in-range variants.
func (c *baseencCodec) spec() *variantSpec {
	//: pointer-into-array avoids cloning the spec on every call.
	return &variantSpecs[c.variant]
}
