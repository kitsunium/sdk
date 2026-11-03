package transform

import (
	"io"
	"math"
	"slices"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
)

// maxDecompressedBytes caps how many plaintext bytes a single Decompress call
// at this layer will materialise. 256 MiB is generous for log/record payloads
// yet bounds a decompression bomb to a known ceiling; callers needing more route
// through the frame layer's ratio-aware guard. A stream exceeding the cap yields
// the scheme's failure sentinel rather than an OOM.
const maxDecompressedBytes int64 = 256 << 20 // 256 MiB

// spillChunkBytes is the size of one spill chunk: what a decode writes into
// once dst's spare capacity is full, so the plaintext is materialised once at
// its final size instead of through a doubling chain. 32 KiB is the DEFLATE
// window, so a chunk holds what one decoder refill can hand over.
const spillChunkBytes int = 32 << 10

// spillKeepChunks is how many chunks one call hands back to spillPool: 1 MiB.
// A larger decode drops the rest, so a single 256 MiB stream cannot leave
// 256 MiB pinned in the pool until the next two collections.
const spillKeepChunks int = 32

// spillChunk is one fixed-size spill buffer. An array, so a chunk is one
// allocation and its pointer is what the pool holds.
type spillChunk [spillChunkBytes]byte

// spillPool recycles spill chunks across decodes of all three schemes: a
// chunk carries plaintext bytes and no pointer, so it is scheme-neutral.
var spillPool = recycler.NewPool(func() *spillChunk { return new(spillChunk) })

// appendAllBounded drains r onto dst, refusing to materialise more than max
// plaintext bytes, and returns dst extended by what it read (kitsunium/sdk#1102).
//
// The plaintext lands in dst's spare capacity first, read in place, so a
// caller handing a dst large enough for the payload pays no allocation and no
// copy. What does not fit is read into pooled 32 KiB chunks, and dst then grows
// ONCE, to its final length, and the chunks are copied in: one allocation of
// the payload's size, where io.ReadAll's doubling chain and the copy onto dst
// after it cost three and a half times the plaintext.
//
// It reads one byte past max, so an over-cap stream is detected as overflow
// rather than silently truncated; the caller maps overflow to its own sentinel.
// The +1 saturates at math.MaxInt64 (see below). On overflow or a read fault it
// returns dst at its original length: the caller's prefix intact and never a
// partial payload — dst's spare capacity may have been written, which the
// port's convention allows.
//
// max is an explicit parameter (production passes maxDecompressedBytes) so the
// overflow path can be exercised against a small cap in tests without
// materialising the production ceiling and without mutating shared state.
func appendAllBounded(dst []byte, r io.Reader, max int64) (decoded []byte, overflow bool, err error) {
	//: the +1 is the whole detector, and it is also the one arithmetic here
	//: that can wrap: at math.MaxInt64 it goes NEGATIVE and a valid stream
	//: decodes to nothing with a nil error — measured once, as gzip
	//: DecompressBounded(src, math.MaxInt64) returning 0 bytes for a 4096-byte
	//: payload. Saturating is sound: no stream exceeds MaxInt64 bytes, so at
	//: that ceiling there is no overflow left to detect.
	limit := max
	//: only widen when the increment still fits.
	if limit < math.MaxInt64 {
		//: the ordinary detector width.
		limit++
	}
	//: first, dst's own spare capacity — the zero-copy path.
	filled, done, err := fillSpare(dst, r, limit)
	//: a read fault is the caller's to wrap; dst goes back as it came.
	if err != nil {
		//: never a partial payload.
		return dst, false, err
	}
	//: the stream ended inside dst's capacity.
	if done {
		//: within the cap unless it read the detector byte.
		decoded, overflow = settle(dst, filled, max)
		return decoded, overflow, nil
	}
	//: dst is full and the stream goes on: spill, then grow once.
	return spill(dst, filled, r, limit, max)
}

// fillSpare reads r into out's spare capacity, never more than limit bytes
// past len(out), and reports whether the stream ended there. It stops at a
// full buffer or at the limit without probing further: spill decides what
// that means.
func fillSpare(out []byte, r io.Reader, limit int64) (filled []byte, done bool, err error) {
	start := len(out)
	//: read in place while there is room and the limit is not reached.
	for len(out) < cap(out) {
		room := out[len(out):cap(out)]
		//: never read past the detector byte.
		if left := limit - int64(len(out)-start); int64(len(room)) > left {
			//: left is smaller than the room here, so it fits an int.
			room = room[:left]
		}
		//: the limit is reached: spill sees it and calls the overflow.
		if len(room) == 0 {
			//: not done — the stream may well go on.
			return out, false, nil
		}
		n, rerr := r.Read(room)
		out = out[:len(out)+n]
		//: the stream ended inside the capacity.
		if rerr == io.EOF {
			//: done, nothing to spill.
			return out, true, nil
		}
		//: a corrupt stream or a transport fault.
		if rerr != nil {
			//: surfaced verbatim; the caller restores dst.
			return out, false, rerr
		}
	}
	//: the capacity is full; whether the stream is over is spill's question.
	return out, false, nil
}

// spill reads the rest of r into pooled chunks, then grows out once to its
// final length and copies them in. A dst that held the payload exactly finds
// io.EOF on its first chunk read and is returned as it is, unreallocated.
func spill(dst, out []byte, r io.Reader, limit, max int64) (decoded []byte, overflow bool, err error) {
	start := len(dst)
	//: the chunk list lives on this frame for the first spillKeepChunks.
	chunks := make([]*spillChunk, 0, spillKeepChunks)
	spilled := 0
	//: read chunk by chunk until the stream ends or the limit is reached.
	for {
		got := int64(len(out)-start) + int64(spilled)
		//: the detector byte is in: more than max plaintext.
		if got > max {
			//: the chunks go back, dst as it came, never a partial payload.
			releaseSpill(chunks)
			return dst, true, nil
		}
		//: a full chunk, or none yet: take the next one.
		if spilled == len(chunks)*spillChunkBytes {
			chunks = append(chunks, spillPool.Get())
		}
		room := chunks[len(chunks)-1][spilled%spillChunkBytes:]
		//: never read past the detector byte.
		if left := limit - got; int64(len(room)) > left {
			//: left is smaller than a chunk here, so it fits an int.
			room = room[:left]
		}
		n, rerr := r.Read(room)
		spilled += n
		//: the stream is over: assemble.
		if rerr == io.EOF {
			break
		}
		//: a corrupt stream or a transport fault.
		if rerr != nil {
			//: the chunks go back, the fault verbatim, dst as it came.
			releaseSpill(chunks)
			return dst, false, rerr
		}
	}
	//: one allocation of the final length — none at all for an exact fit,
	//: exactly the payload when dst was nil, append's growth otherwise.
	out = slices.Grow(out, spilled)
	//: copy the chunks in, in order; the last one is partly full.
	for i, chunk := range chunks {
		out = append(out, chunk[:min(spilled-i*spillChunkBytes, spillChunkBytes)]...)
	}
	releaseSpill(chunks)
	//: within the cap unless it read the detector byte.
	decoded, overflow = settle(dst, out, max)
	return decoded, overflow, nil
}

// settle reports overflow when out holds more than max bytes past dst, and
// hands out back otherwise.
func settle(dst, out []byte, max int64) (decoded []byte, overflow bool) {
	//: the detector byte made it in: the stream wanted more than max.
	if int64(len(out)-len(dst)) > max {
		//: dst as it came, never a partial payload.
		return dst, true
	}
	//: within bounds — the payload appended onto dst.
	return out, false
}

// releaseSpill hands the first spillKeepChunks chunks back to the pool and
// drops the rest, so one large decode cannot leave its whole plaintext pinned
// in the pool.
func releaseSpill(chunks []*spillChunk) {
	//: bounded retention: what one call may leave behind.
	for _, chunk := range chunks[:min(len(chunks), spillKeepChunks)] {
		//: a chunk carries bytes only, so it needs no reset.
		spillPool.Put(chunk)
	}
}
