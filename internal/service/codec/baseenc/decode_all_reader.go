// Package baseenc — buffered decode reader for variants whose stdlib has
// no streaming decoder (Base45). Held in its own file so KTN-STRUCT-ONEFILE
// sees exactly one struct per file.
package baseenc

import (
	"bytes"
	"io"
)

// decodeAllReader adapts a non-streamable base-N variant to io.Reader: on
// the first Read it drains the whole source, runs the variant's decodeBytes
// transform, and serves the decoded payload from an in-memory reader.
// Decode errors surface on that first Read. Used by Base45, which has no
// incremental stdlib decoder.
type decodeAllReader struct {
	src  io.Reader
	dec  *bytes.Reader
	v    variant
	done bool
}

// Read drains+decodes on first call, then serves the decoded bytes.
func (r *decodeAllReader) Read(p []byte) (n int, err error) {
	//: first call materialises the decoded payload.
	if !r.done {
		//: drain the whole base-N source.
		raw, rerr := io.ReadAll(r.src)
		//: surface a source read failure verbatim.
		if rerr != nil {
			//: caller sees the underlying read error.
			return 0, rerr
		}
		//: run the variant's decode transform (already wraps its errors).
		decoded, derr := (&baseencCodec{variant: r.v}).decodeBytes(raw)
		//: hand a wrapped decode error to the caller.
		if derr != nil {
			//: dotted-quad code already attached by decodeBytes.
			return 0, derr
		}
		//: serve the decoded bytes from here on.
		r.dec = bytes.NewReader(decoded)
		r.done = true
	}
	//: delegate to the in-memory reader.
	return r.dec.Read(p)
}
