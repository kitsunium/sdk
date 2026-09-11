// Package transform declares the byte-transform port of the SDK: the
// Compressor contract and the typed Algorithm string under which a compressor
// registers. It is the fifth core sibling beside codec, writer, crypto, and
// logger (ADR 0014 D1) and mirrors codec exactly — the registry resolves an
// Algorithm to a Compressor the way codec resolves a Format to a Codec.
//
// No algorithm bodies and no vendor types live here; concrete compressors live
// under internal/service/transform/ (stdlib gzip/flate/zlib today) and
// self-register via a package-level var initialiser when imported — no init().
package transform

// Algorithm is the typed key under which a Compressor registers (e.g. "gzip",
// "flate", "zlib"). The zero value Algorithm("") is reserved invalid, mirroring
// codec.Format and crypto.Algorithm. Note that "flate" is the raw DEFLATE
// stream of RFC 1951 and "zlib" is the RFC 1950 envelope HTTP misnames
// "deflate" — distinct Algorithms, not aliases.
type Algorithm string

// String implements fmt.Stringer and returns the raw identifier.
func (a Algorithm) String() string {
	//: direct cast from the typed string back to a plain string.
	return string(a)
}

// Known reports whether a has been registered in the Compressor registry.
func (a Algorithm) Known() bool {
	//: empty Algorithms are reserved as the invalid zero value.
	if a == "" {
		//: nothing can match the empty Algorithm.
		return false
	}
	//: delegate to the registry for the actual lookup.
	_, found := Lookup(a)
	//: propagate the registry's verdict.
	return found
}

// Compressor is the contract every byte-transform scheme satisfies. Instances
// MUST be safe for concurrent use; scheme-specific options are supplied via the
// scheme's constructor, not via the Compressor interface. Compress and
// Decompress follow the append-to-dst convention of the stdlib (dst may be nil)
// so callers can reuse buffers on the hot path.
//
// src must not overlap dst's spare capacity, dst[len(dst):cap(dst)]: every
// scheme writes its output there while it is still reading src, so an aliased
// src is overwritten mid-read and the output is corrupt. Reusing one buffer
// means passing buf[:0] as dst and a DIFFERENT buffer as src — the same rule
// as copy-free encoders across the stdlib, stated here because no scheme can
// check it cheaply.
//
// IFACE-PLUGIN: the registry stores plug-in scheme instances behind this
// interface — concrete scheme types stay unexported per package.
type Compressor interface {
	Algorithm() Algorithm
	Compress(dst, src []byte) (out []byte, err error)
	Decompress(dst, src []byte) (out []byte, err error)
}
