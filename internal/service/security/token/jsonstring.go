package token

import "github.com/kitsunium/sdk/internal/kernel/jsonenc"

// quoteScratch is the stack buffer a literal is built in: an issuer, an
// audience or a key id fits, so the one allocation is the string returned.
const quoteScratch int = 128

// quoteJSONString renders s as a JSON string literal, escaped by
// kernel/jsonenc: the same rules as every other JSON emitter of the SDK.
//
// Every value it renders today comes from the SDK's own configuration or from
// a claim this package already decoded — never straight off the wire. It
// escapes anyway, because a string concatenated into JSON is exactly the shape
// that stops being safe the day somebody makes one of its inputs dynamic.
//
// It REPAIRS invalid UTF-8 (each bad byte becomes U+FFFD) where the byte loop
// it replaced copied the byte and left a literal no strict reader accepts. The
// issuer does not rely on that repair: checkClaimText refuses claim text that
// is not UTF-8 before anything is rendered, because a repair would mint
// "a\xff" and "a\xfe" as two tokens a reader decodes to one subject.
func quoteJSONString(s string) string {
	var scratch [quoteScratch]byte
	//: a valid JSON string literal.
	return string(jsonenc.AppendString(scratch[:0], s))
}

// quoteJSONStrings renders values as a JSON array of string literals.
func quoteJSONStrings(values []string) string {
	var scratch [quoteScratch]byte
	out := append(scratch[:0], '[')
	//: comma-separated, in the caller's order.
	for i, value := range values {
		//: every element but the first needs its separator.
		if i > 0 {
			out = append(out, ',')
		}
		out = jsonenc.AppendString(out, value)
	}
	//: a valid JSON array literal.
	return string(append(out, ']'))
}
