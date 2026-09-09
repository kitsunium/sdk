// Package codec — compressed-frame verbs (ADR 0014 D1). MarshalCompressed and
// UnmarshalCompressed wrap the universal codec dispatch in a self-describing,
// version-tagged compression frame, so any value can be compressed in any
// registered Format without a parallel API. Compression is a parallel transform
// registry, never a codec Format (ADR 0014 §Why-not), so the algorithm is named
// separately from the wire Format.
package codec

import (
	"encoding/binary"

	coretransform "github.com/kitsunium/sdk/internal/core/transform"
	_ "github.com/kitsunium/sdk/internal/service/transform" // self-registers gzip + flate + zlib
)

const (
	// frameMagic is the first byte of every compressed frame — a sentinel that
	// lets UnmarshalCompressed reject non-frame input before doing any work.
	frameMagic byte = 0xC7
	// frameVersion is the frame layout version, frozen post-v1.0.0; a future
	// layout change bumps it and UnmarshalCompressed rejects an unknown version.
	frameVersion byte = 0x01

	// algGzip is the frozen 1-byte frame id for the gzip compressor.
	algGzip byte = 0x01
	// algFlate is the frozen 1-byte frame id for the raw-DEFLATE compressor.
	algFlate byte = 0x02

	// algIDIndex is the byte offset of the algorithm id within a frame header.
	algIDIndex int = 2
	// formatLenStart is the byte offset of the 2-byte inner-Format length field.
	formatLenStart int = 3
	// frameHeaderLen is the fixed prefix before the variable inner Format:
	// magic + version + algID + a 2-byte inner-Format length.
	frameHeaderLen int = 5
	// maxInnerFormatLen is the largest inner Format the 2-byte length addresses.
	maxInnerFormatLen int = 0xFFFF

	// maxExpansionRatio bounds decompressed/compressed size; above it (and above
	// the small-payload floor) the frame is treated as a decompression bomb.
	maxExpansionRatio int = 1000
	// bombFloorBytes lets tiny legitimate payloads expand freely below this size,
	// so the ratio guard never rejects small inputs with naturally high ratios.
	bombFloorBytes int = 4 << 10 // 4 KiB
	// maxDecompressedFrameBytes is the absolute frame-layer ceiling (tighter than
	// the service layer's 256 MiB backstop) above which output is a bomb.
	maxDecompressedFrameBytes int = 64 << 20 // 64 MiB
)

// CompressAlgorithm re-exports core/transform.Algorithm so consumers name a
// compressor without importing internal/*. Use the Gzip / Flate constants.
type CompressAlgorithm = coretransform.Algorithm

const (
	// Gzip selects the gzip compressor (frame algID 0x01).
	Gzip CompressAlgorithm = "gzip"
	// Flate selects the raw-DEFLATE compressor (frame algID 0x02).
	Flate CompressAlgorithm = "flate"
)

// MarshalCompressed serialises v with the codec registered under f, compresses
// the result with the transform registered under algo, and wraps both in a
// self-describing frame so UnmarshalCompressed needs no Format or Algorithm
// argument. It returns UnknownCompressor when algo has no frame id or no
// registered body, CompressedFrameInvalid when f exceeds the addressable length,
// and forwards any codec / compressor error untouched (origin wins).
func MarshalCompressed(f Format, algo CompressAlgorithm, v any) (box []byte, err error) {
	//: resolve the frozen frame id first so an unaddressable algorithm fails fast.
	id, ok := frameAlgID(algo)
	//: an algorithm with no frame id cannot be encoded in this frame version.
	if !ok {
		//: name the missing compressor via the core sentinel.
		return nil, coretransform.UnknownCompressor
	}
	//: an over-long Format name cannot fit the 2-byte length field.
	if len(f) > maxInnerFormatLen {
		//: a Format this long is a malformed request, not a valid frame.
		return nil, coretransform.CompressedFrameInvalid
	}
	//: encode v through the universal codec dispatch (reuses promotion).
	encoded, mErr := Marshal(f, v)
	//: a codec failure is forwarded untouched — origin wins.
	if mErr != nil {
		//: pass the codec error through.
		return nil, mErr
	}
	//: resolve + run the compressor, then render the frame.
	return compressFrame(f, id, encoded)
}

// compressFrame resolves the compressor for the frame id's algorithm, compresses
// payload, and renders the header + compressed body. Split out of
// MarshalCompressed to keep each function within the line budget.
func compressFrame(f Format, id byte, payload []byte) (box []byte, err error) {
	//: the id was just validated, so the inverse lookup cannot miss.
	algo, _ := algorithmForID(id)
	//: resolve the compressor; an unregistered algorithm is a missing import.
	c, ok := coretransform.Lookup(algo)
	//: a framed algorithm with no registered body — name the miss.
	if !ok {
		//: surface the documented sentinel.
		return nil, coretransform.UnknownCompressor
	}
	//: compress; the scheme wraps its own stdlib fault (origin wins).
	compressed, cErr := c.Compress(nil, payload)
	//: forward a compression fault untouched.
	if cErr != nil {
		//: pass the compressor error through.
		return nil, cErr
	}
	//: assemble the header then append the compressed payload.
	return appendFrame(id, f, compressed), nil
}

// appendFrame renders [magic][ver][algID][2B innerFormatLen BE][innerFormat]
// followed by the compressed payload. The frame is self-describing so the reader
// recovers the inner Format without a side channel.
func appendFrame(id byte, f Format, compressed []byte) []byte {
	//: pre-size exactly: fixed header + Format bytes + compressed payload.
	box := make([]byte, 0, frameHeaderLen+len(f)+len(compressed))
	//: magic + version + algorithm id are the fixed leading bytes.
	box = append(box, frameMagic, frameVersion, id)
	//: the 2-byte big-endian inner-Format length precedes the Format bytes.
	box = binary.BigEndian.AppendUint16(box, uint16(len(f)))
	//: the inner Format string lets the reader pick the codec on decode.
	box = append(box, []byte(f)...)
	//: the compressed codec output is the frame body.
	return append(box, compressed...)
}

// UnmarshalCompressed parses a frame produced by MarshalCompressed, decompresses
// the payload under a bomb guard, and decodes it into v using the inner Format
// the frame records. A malformed frame, an unknown algorithm id, or a payload
// that decompresses past the bomb guard returns CompressedFrameInvalid; codec
// and compressor faults are forwarded untouched.
func UnmarshalCompressed(box []byte, v any) error {
	//: parse + validate the header, recovering the algorithm and inner Format.
	algo, f, payload, ok := parseFrame(box)
	//: a header that fails any structural check is frame corruption.
	if !ok {
		//: typed, non-oracle frame-invalid sentinel.
		return coretransform.CompressedFrameInvalid
	}
	//: decompress under the frame-layer bomb guard.
	plain, err := decompressBounded(algo, payload)
	//: decode only on a clean decompress; a fault is forwarded as-is.
	if err == nil {
		//: decode the recovered plaintext into v via the universal dispatch.
		err = Unmarshal(f, plain, v)
	}
	//: a decompression or decode fault is returned untouched (origin wins).
	return err
}

// parseFrame validates the fixed header + length field and slices out the inner
// Format and compressed payload. It reports ok=false for any structural defect:
// short input, wrong magic/version, an unknown algorithm id, or a length field
// that overruns the buffer.
func parseFrame(box []byte) (algo CompressAlgorithm, f Format, payload []byte, ok bool) {
	//: the buffer must span the fixed header and carry the right magic/version.
	if len(box) < frameHeaderLen || box[0] != frameMagic || box[1] != frameVersion {
		//: too short, or not a frame this version produced.
		return "", "", nil, false
	}
	//: the algorithm-id byte must resolve to a registered algorithm name.
	resolved, known := algorithmForID(box[algIDIndex])
	//: an unknown id is an unsupported / corrupt frame.
	if !known {
		//: reject.
		return "", "", nil, false
	}
	//: the big-endian inner-Format length follows the algorithm id.
	nameLen := int(binary.BigEndian.Uint16(box[formatLenStart:frameHeaderLen]))
	//: the Format bytes (and a possibly-empty payload) must fit the buffer.
	if frameHeaderLen+nameLen > len(box) {
		//: the length field overruns the buffer — corruption.
		return "", "", nil, false
	}
	//: slice the inner Format, then the remaining compressed payload.
	return resolved, Format(box[frameHeaderLen : frameHeaderLen+nameLen]), box[frameHeaderLen+nameLen:], true
}

// decompressBounded resolves the compressor for algo, decompresses payload, and
// enforces the frame-layer decompression-bomb guard. A bomb is reported as
// CompressedFrameInvalid; a missing compressor or a stdlib fault is forwarded.
func decompressBounded(algo CompressAlgorithm, payload []byte) (plain []byte, err error) {
	//: resolve the compressor; pkg/v1/codec always imports gzip + flate.
	c, ok := coretransform.Lookup(algo)
	//: a framed algorithm with no body means a missing import — name the miss.
	if !ok {
		//: surface the documented sentinel.
		return nil, coretransform.UnknownCompressor
	}
	//: decompress; the service layer bounds output at its 256 MiB backstop.
	out, dErr := c.Decompress(nil, payload)
	//: forward a stdlib decode fault (corrupt body / truncation) untouched.
	if dErr != nil {
		//: pass the compressor error through.
		return nil, dErr
	}
	//: a frame whose output trips the bomb guard is rejected as frame-invalid.
	if isBomb(len(payload), len(out)) {
		//: typed, non-oracle bomb rejection.
		return nil, coretransform.CompressedFrameInvalid
	}
	//: within bounds — hand back the plaintext.
	return out, nil
}

// isBomb reports whether an output of outLen bytes from a payload of inLen bytes
// trips the decompression-bomb guard: either it exceeds the absolute frame
// ceiling, or it exceeds the expansion-ratio bound while above the small-payload
// floor (small inputs may expand freely so the ratio never rejects them).
func isBomb(inLen, outLen int) bool {
	//: the absolute ceiling is an unconditional rejection.
	if outLen > maxDecompressedFrameBytes {
		//: over the hard ceiling — a bomb.
		return true
	}
	//: below the small-payload floor the ratio guard does not apply.
	if outLen <= bombFloorBytes {
		//: tiny outputs are always allowed.
		return false
	}
	//: above the floor, bound the expansion ratio against the compressed size.
	return outLen > inLen*maxExpansionRatio
}

// frameAlgID maps a CompressAlgorithm to its frozen 1-byte frame id, reporting
// false for an algorithm with no assigned id (a newer scheme this frame version
// cannot address).
func frameAlgID(algo CompressAlgorithm) (id byte, ok bool) {
	//: gzip is the first frozen scheme.
	if algo == Gzip {
		//: gzip owns frame id 0x01.
		return algGzip, true
	}
	//: flate is the second frozen scheme.
	if algo == Flate {
		//: flate owns frame id 0x02.
		return algFlate, true
	}
	//: any other algorithm has no id in this frame version.
	return 0, false
}

// algorithmForID maps a frozen frame id back to its CompressAlgorithm, reporting
// false for an id this frame version does not define (the inverse of frameAlgID).
func algorithmForID(id byte) (algo CompressAlgorithm, ok bool) {
	//: 0x01 is the gzip frame id.
	if id == algGzip {
		//: map it back to the gzip algorithm.
		return Gzip, true
	}
	//: 0x02 is the flate frame id.
	if id == algFlate {
		//: map it back to the flate algorithm.
		return Flate, true
	}
	//: any other id is unsupported in this frame version.
	return "", false
}
