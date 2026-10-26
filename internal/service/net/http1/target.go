package http1

import (
	"bytes"
	"slices"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

const (
	// schemeHTTP and schemeHTTPS are the schemes an absolute-form target may
	// name, lowercase: foldIs compares a target's scheme to them whatever its
	// case.
	schemeHTTP string = "http://"
	// schemeHTTPS is "https://".
	schemeHTTPS string = "https://"
	// noDigit is hexDigits' value for a byte that is no hexadecimal digit.
	noDigit byte = 0xFF
	// decimalDigits is how many digits 0-9 are.
	decimalDigits int = 10
	// hexLetters is how many letters a-f are.
	hexLetters int = 6
	// escapeLen is the length of a percent-escape: '%' and two digits.
	escapeLen int = 3
)

var (
	// hexDigits are the values of the hexadecimal digits, by byte; noDigit for
	// any other.
	hexDigits = hexTable()
	// pathRoot is the canonical path of an absolute-form target with none.
	pathRoot = slices.Clip([]byte("/"))
	// pathAsterisk is the canonical path of the asterisk-form target.
	pathAsterisk = slices.Clip([]byte("*"))
	// methodOptions is the one method an asterisk-form target may carry.
	methodOptions = []byte("OPTIONS")
	// querySep starts a target's query.
	querySep = []byte("?")
)

// hexTable builds hexDigits.
func hexTable() [byteValues]byte {
	var t [byteValues]byte
	//: every byte starts as no digit.
	for i := range t {
		t[i] = noDigit
	}
	//: 0-9, a-f and A-F.
	for i := range decimalDigits {
		t['0'+i] = byte(i)
	}
	//: both cases of the six letters.
	for i := range hexLetters {
		t['a'+i], t['A'+i] = byte(decimalDigits+i), byte(decimalDigits+i)
	}
	//: the table.
	return t
}

// canonical computes req.Path and req.Query from req.Target once the head is
// whole, since an absolute-form target is judged against the Host field. It
// refuses a target two readers could resolve to two resources: a dot-segment,
// an empty segment, a backslash, an invalid escape, an encoded slash, dot,
// backslash or NUL (RefusalTargetPath); a form the profile does not serve
// (RefusalTargetForm); an absolute-form authority that is not the Host's
// (RefusalTargetHost). Nothing is decoded and nothing allocates: both slices
// alias the target, or a constant.
func canonical(req *corenet.HTTP1RequestValue) Refusal {
	t := req.Target
	//: by the target's first byte, which the four forms differ by.
	switch {
	//: origin-form: the common case.
	case t[0] == '/':
		//: path and query.
		return splitPath(t, req)
	//: asterisk-form, OPTIONS only (RFC 9112 §3.2.4).
	case len(t) == 1 && t[0] == '*':
		//: any other method with "*" names no resource.
		if !bytes.Equal(req.Method, methodOptions) {
			//: refused.
			return RefusalTargetForm
		}
		req.Path = pathAsterisk
		//: the server as a whole.
		return RefusalNone
	//: absolute-form, or nothing the profile serves.
	default:
		//: the authority judged against Host, then the path.
		return absoluteForm(t, req)
	}
}

// absoluteForm reduces an absolute-form target to its path once its
// authority matched the Host field, A–Z folded (RFC 9112 §3.2.2: a server
// that kept both could route by one and authorise by the other).
func absoluteForm(t []byte, req *corenet.HTTP1RequestValue) Refusal {
	rest, ok := cutScheme(t)
	//: authority-form is CONNECT's, refused earlier; nothing else is a target.
	if !ok {
		//: refused.
		return RefusalTargetForm
	}
	end := len(rest)
	//: the authority ends at the path or the query.
	if i := bytes.IndexAny(rest, "/?"); i >= 0 {
		end = i
	}
	//: no authority, userinfo, no Host to match, or another one.
	if !authorityIsHost(rest[:end], req) {
		//: refused.
		return RefusalTargetHost
	}
	tail := rest[end:]
	//: a path from "/", judged like an origin-form one.
	if len(tail) > 0 && tail[0] == '/' {
		//: path and query.
		return splitPath(tail, req)
	}
	req.Path = pathRoot
	//: an empty path is the root; the query, if any, after its "?".
	if len(tail) > 0 {
		req.Query = tail[1:]
	}
	//: reduced.
	return RefusalNone
}

// cutScheme returns what follows an http:// or https:// scheme, in any case,
// and false for any other target.
func cutScheme(t []byte) ([]byte, bool) {
	//: the two schemes the profile serves.
	for _, scheme := range [...]string{schemeHTTP, schemeHTTPS} {
		//: the scheme and at least one byte after it.
		if len(t) > len(scheme) && foldIs(t[:len(scheme)], scheme) {
			//: what follows it.
			return t[len(scheme):], true
		}
	}
	//: neither.
	return nil, false
}

// authorityIsHost reports whether an absolute-form authority is non-empty,
// carries no userinfo, and equals the request's Host field, A–Z folded.
func authorityIsHost(authority []byte, req *corenet.HTTP1RequestValue) bool {
	//: all four, or the target names another origin than the Host.
	return len(authority) > 0 && bytes.IndexByte(authority, '@') < 0 && req.HasHost && foldEqualBytes(authority, req.Host)
}

// splitPath cuts p, which starts with "/", at its query and checks the path.
func splitPath(p []byte, req *corenet.HTTP1RequestValue) Refusal {
	path, query, found := bytes.Cut(p, querySep)
	//: the query starts after the first "?".
	if found {
		req.Query = query
	}
	//: a path two readers could resolve apart.
	if r := checkPath(path); r != RefusalNone {
		//: refused.
		return r
	}
	req.Path = path
	//: canonical.
	return RefusalNone
}

// checkPath refuses a path, which starts with "/", holding an empty segment
// but the last, a "." or ".." segment, a backslash, an invalid escape, or an
// escape of '/', '.', '\' or NUL.
func checkPath(p []byte) Refusal {
	start := 1
	//: one byte at a time, a segment ending at each "/" and at the end.
	for i := 1; i <= len(p); {
		//: a segment ends.
		if i == len(p) || p[i] == '/' {
			//: empty but last ("//"), or a dot-segment.
			if (i == start && i < len(p)) || dotSegment(p[start:i]) {
				//: refused.
				return RefusalTargetPath
			}
			i++
			start = i
			continue
		}
		n := pathByte(p, i)
		//: a backslash, or an escape the profile refuses.
		if n == 0 {
			//: refused.
			return RefusalTargetPath
		}
		i += n
	}
	//: canonical.
	return RefusalNone
}

// pathByte returns how many bytes of p at i one path byte takes — three for
// an escape, one otherwise — or 0 for a backslash or a refused escape.
func pathByte(p []byte, i int) int {
	//: by the byte, the two that can change what a path names.
	switch p[i] {
	//: a byte some readers take for a separator.
	case '\\':
		//: refused.
		return 0
	//: an escape: two hexadecimal digits, decoding to none of the four.
	case '%':
		//: invalid, or one of the four.
		if !escapeAllowed(p, i) {
			//: refused.
			return 0
		}
		//: the '%' and its digits.
		return escapeLen
	//: any other byte the target scan let through.
	default:
		//: one.
		return 1
	}
}

// dotSegment reports whether seg is "." or "..".
func dotSegment(seg []byte) bool {
	//: one or two dots, nothing else.
	return (len(seg) == 1 && seg[0] == '.') || (len(seg) == 2 && seg[0] == '.' && seg[1] == '.')
}

// escapeAllowed reports whether p holds two hexadecimal digits after the '%'
// at i, decoding to a byte other than '/', '.', '\' and NUL.
func escapeAllowed(p []byte, i int) bool {
	//: the two digits must be there.
	if i+2 >= len(p) {
		//: a truncated escape.
		return false
	}
	hi, lo := hexDigits[p[i+1]], hexDigits[p[i+2]]
	//: both digits valid.
	if hi == noDigit || lo == noDigit {
		//: not an escape.
		return false
	}
	c := hi<<4 | lo
	//: the four bytes whose decoding changes what the path names.
	return c != '/' && c != '.' && c != '\\' && c != 0
}

// foldEqualBytes reports whether a and b are equal once A–Z are folded.
func foldEqualBytes(a, b []byte) bool {
	//: a length mismatch settles it.
	if len(a) != len(b) {
		//: never equal.
		return false
	}
	//: byte by byte; only A–Z fold.
	for i := range len(a) {
		//: one differing byte.
		if lowerASCII(a[i]) != lowerASCII(b[i]) {
			//: not equal.
			return false
		}
	}
	//: equal.
	return true
}

// lowerASCII maps A–Z onto a–z and every other byte onto itself.
func lowerASCII(c byte) byte {
	//: only the 26 capitals move.
	if c >= 'A' && c <= 'Z' {
		//: 0x20 above.
		return c + 'a' - 'A'
	}
	//: as is.
	return c
}
