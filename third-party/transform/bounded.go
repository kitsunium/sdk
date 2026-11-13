package transform

import (
	"bytes"
	"errors"
	"io"
	"math"
	"slices"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
)

// boundedReadStep is the most a stream read of DecompressBounded grows dst by
// at once, when no frame header announced the size: the output grows as it
// arrives, never by the ceiling up front.
const boundedReadStep int64 = 64 << 10

// DecompressBounded implements core/data/transform.BoundedDecompressor: it
// decodes src and appends the result to dst under the CALLER's ceiling max,
// so a caller that expects a tighter bound than the compressor's own stops
// the work at its bound instead of materialising the compressor's and
// judging afterwards.
//
// The output is bounded before it is allocated. A frame whose header
// announces more than max is refused from the header, before a byte is
// decoded; a frame that announces its size is decoded into a buffer of
// exactly that size, which the decoder may not outgrow; any other input —
// a frame without a size, a second frame — is read through the bounded
// stream, which hands over no byte past max. A negative max is read as
// zero, which admits an empty payload and nothing else.
//
// A payload over max fails with core/data/transform.DecompressedTooLarge.
// The compressor's own ceiling stays a backstop: a max above it decodes no
// more than it, refused DecompressionLimitExceeded as Decompress refuses. A
// refusal returns dst at its original length — never a partial payload.
func (z *ZstdCompressor) DecompressBounded(dst, src []byte, max int64) (decoded []byte, err error) {
	ceiling, backstop := z.ceilingOf(max)
	size, sized := announcedSize(src)
	//: announced past the ceiling: refused from the header, nothing decoded.
	if sized && size > uint64(ceiling) {
		return dst, z.overCeiling(backstop, src)
	}
	//: announced within it: decoded into exactly the announced size, when an
	//: int holds it (a 32-bit build under a ceiling past 2 GiB streams it).
	if sized && size <= uint64(math.MaxInt) {
		out, done, derr := z.decodeSized(dst, src, int(size))
		if done {
			return out, derr
		}
	}
	//: no size announced, or more than one frame: the bounded stream.
	return z.decodeBoundedStream(dst, src, ceiling, backstop)
}

// ceilingOf is the ceiling DecompressBounded applies for the caller's max:
// below zero there is no buffer a caller could be asking for, and the
// compressor's own ceiling is the backstop above the caller's, which backstop
// reports.
func (z *ZstdCompressor) ceilingOf(max int64) (ceiling int64, backstop bool) {
	ceiling = max
	if ceiling < 0 {
		ceiling = 0
	}
	if ceiling > z.maxDecompressedBytes {
		return z.maxDecompressedBytes, true
	}
	return ceiling, false
}

// announcedSize is the content size the first frame of src announces, and
// whether it announces one: a skippable frame or a header without a size
// does not.
func announcedSize(src []byte) (size uint64, sized bool) {
	var header zstd.Header
	if header.Decode(src) != nil || header.Skippable || !header.HasFCS {
		return 0, false
	}
	return header.FrameContentSize, true
}

// overCeiling is the refusal of a payload past the ceiling DecompressBounded
// applied: the caller's, DecompressedTooLarge, or the compressor's own when
// it was the tighter, DecompressionLimitExceeded as Decompress reports it.
func (z *ZstdCompressor) overCeiling(backstop bool, src []byte) error {
	//: the compressor's own ceiling refused it.
	if backstop {
		return z.classify(zstd.ErrDecoderSizeExceeded, src)
	}
	return coretransform.DecompressedTooLarge
}

// decodeSized decodes src, whose first frame announced size bytes, into dst's
// tail capped at exactly size, with the decoder that never writes past its
// buffer's capacity. done is false when the input did not fit — a second
// frame, or a frame that lied about its size —, which only the stream can
// tell apart, and dst is then untouched.
func (z *ZstdCompressor) decodeSized(dst, src []byte, size int) (decoded []byte, done bool, err error) {
	start := len(dst)
	out := dst
	//: room for the announced size and no more, in dst's array when it has it.
	if cap(out)-start < size {
		out = slices.Grow(out, size)
	}
	tail := out[start:start:(start + size)]
	got, derr := z.capped.DecodeAll(src, tail)
	//: the capacity refused it: past this frame, or a frame that lied.
	if errors.Is(derr, zstd.ErrDecoderSizeExceeded) || errors.Is(derr, zstd.ErrFrameSizeExceeded) {
		return dst, false, nil
	}
	//: a corrupt frame, a dictionary this compressor does not hold.
	if derr != nil {
		return dst, true, z.classify(derr, src)
	}
	//: the decode stayed in out's array, capped at its capacity.
	return out[:start+len(got)], true, nil
}

// decodeBoundedStream reads src through the bounded stream NewReader returns,
// appending to dst as the output arrives: dst grows by at most
// boundedReadStep at once and never past ceiling, and the byte past ceiling
// is refused without being handed over.
func (z *ZstdCompressor) decodeBoundedStream(dst, src []byte, ceiling int64, backstop bool) (decoded []byte, err error) {
	start := len(dst)
	rc, rerr := z.NewReader(bytes.NewReader(src), ceiling)
	//: the first frame's header refused, or an option fault.
	if rerr != nil {
		return dst, rerr
	}
	defer func() {
		//: the decoder goes back to its pool; Close reports nothing today,
		//: and a failure it came to report would not be lost.
		if cerr := rc.Close(); cerr != nil && err == nil {
			decoded, err = dst, cerr
		}
	}()
	out := dst
	var probe [1]byte
	for {
		left := ceiling - int64(len(out)-start)
		var p []byte
		p, out = readWindow(out, left, probe[:])
		n, readErr := rc.Read(p)
		//: at the ceiling, p is the probe and the stream refuses a byte.
		if left > 0 {
			out = out[:len(out)+n]
		}
		if readErr == io.EOF {
			return out, nil
		}
		if readErr != nil {
			return dst, z.streamRefusal(readErr, backstop, src)
		}
	}
}

// readWindow is the room the next stream read fills: out grown a step when it
// is full, and the free tail of it capped at left, the bytes the ceiling
// still admits. At the ceiling it is the one-byte probe instead, so that the
// stream refuses a byte past it rather than handing it over.
func readWindow(out []byte, left int64, probe []byte) (window, grown []byte) {
	if left <= 0 {
		return probe, out
	}
	//: room for what may still come, a step at a time.
	if cap(out) == len(out) {
		out = slices.Grow(out, int(min(left, boundedReadStep)))
	}
	return out[len(out):int(min(int64(cap(out)), int64(len(out))+left))], out
}

// streamRefusal maps the bounded stream's refusal of a byte past the
// ceiling onto DecompressBounded's: the compressor's own ceiling reported as
// Decompress reports it, the caller's as DecompressedTooLarge. Any other
// error is the stream's, already classified.
func (z *ZstdCompressor) streamRefusal(cause error, backstop bool, src []byte) error {
	//: the ceiling, not the stream, refused it.
	if errors.Is(cause, coretransform.DecompressedTooLarge) {
		return z.overCeiling(backstop, src)
	}
	//: a corrupt stream, a window past the compressor's ceiling.
	return cause
}

// DecompressBounded implements core/data/transform.BoundedDecompressor for
// s2: the block header declares the plaintext length, so a block declaring
// more than max is refused from the header, before a byte is allocated or
// decoded, with core/data/transform.DecompressedTooLarge. A negative max is
// read as zero. The compressor's own ceiling stays a backstop, refused
// DecompressionLimitExceeded as Decompress refuses. A refusal returns dst at
// its original length — never a partial payload.
func (s *S2Compressor) DecompressBounded(dst, src []byte, max int64) (decoded []byte, err error) {
	//: below zero there is no buffer a caller could be asking for.
	ceiling := max
	if ceiling < 0 {
		ceiling = 0
	}
	want, lerr := s2.DecodedLen(src)
	//: a corrupt or absent header is a malformed stream, as Decompress says.
	if lerr != nil {
		return s.Decompress(dst, src)
	}
	//: the caller's ceiling, below the compressor's own, refuses from the header.
	if int64(want) > ceiling && ceiling < s.maxDecompressedBytes {
		return dst, coretransform.DecompressedTooLarge
	}
	//: within it, or past the compressor's own: Decompress decides.
	return s.Decompress(dst, src)
}
