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

// streamDecodeLimit is the maximum encoded bytes decodeAllReader will buffer
// before refusing the input: the same caps the non-streaming Unmarshal applies,
// so a hostile stream cannot bypass them via NewDecoder. Base-conversion
// variants use the (expansion-aware) encoded cap; block variants the 10 MiB cap.
func streamDecodeLimit(v variant) int {
	//: base58/62 are O(n²) — bound the encoded text at the expansion-aware cap.
	if isBaseConversion(v) {
		//: the same ceiling Unmarshal enforces on the encoded side.
		return maxConvEncodedBytes
	}
	//: block variants (base45) share the 10 MiB CWE-400 cap.
	return maxBaseEncBytes
}

// Read drains+decodes on first call, then serves the decoded bytes.
func (r *decodeAllReader) Read(p []byte) (n int, err error) {
	//: first call materialises the decoded payload.
	if !r.done {
		//: cap the drain so a hostile stream cannot bypass the size limit; read
		//: one byte past the cap to detect (not silently truncate) an overflow.
		limit := streamDecodeLimit(r.v)
		//: drain the base-N source up to limit+1 bytes.
		raw, rerr := io.ReadAll(io.LimitReader(r.src, int64(limit)+1))
		//: surface a source read failure verbatim.
		if rerr != nil {
			//: caller sees the underlying read error.
			return 0, rerr
		}
		//: more than limit bytes means the stream exceeds the decode cap.
		if len(raw) > limit {
			//: refuse before the (possibly quadratic) decode runs.
			return 0, convSizeExceeded("service/codec/baseenc: streaming decode input exceeds the size cap")
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
