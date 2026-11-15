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
// The ceiling outranks the fault (ADR 0213, kitsunium/sdk#1432): a decoder
// hands over the last bytes of a cut stream in the same return as its sticky
// fault, so a read that brings the detector byte and a fault together is an
// overflow, and the fault is not reported. A fault is reported only when the
// stream gave no more than max bytes before it (see ceilingFirst).
//
// A dst that holds the payload exactly leaves the stream unread at its end, and
// the end is found by asking for ONE byte into probe — the decoder's own box
// owns it, so asking costs nothing — and never by taking a 32 KiB spill chunk
// for the question: a chunk the pool did not have (a collection, a goroutine
// migrated to another P) was a 32 768-byte allocation in a call that decodes
// into place, measured as the 1 B/op the sized rows still showed. A byte the
// probe does find starts the first chunk. A nil probe is allocated, for the
// callers that decode no box.
//
// max is an explicit parameter (production passes maxDecompressedBytes) so the
// overflow path can be exercised against a small cap in tests without
// materialising the production ceiling and without mutating shared state.
func appendAllBounded(dst []byte, r io.Reader, probe *[1]byte, max int64) (decoded []byte, overflow bool, err error) {
	//: a caller with no box to own the probe byte gets one of its own.
	if probe == nil {
		//: only the callers that decode no box reach this.
		probe = new([1]byte)
	}
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
		//: never a partial payload; the ceiling first if the fault came past it.
		overflow, err = ceilingFirst(err, int64(len(filled)-len(dst)), max)
		return dst, overflow, err
	}
	//: the stream ended inside dst's capacity.
	if done {
		//: within the cap unless it read the detector byte.
		decoded, overflow = settle(dst, filled, max)
		return decoded, overflow, nil
	}
	//: dst is full, or the limit is reached; spill settles which, then grows once.
	return spill(dst, filled, &boundedRead{r: r, probe: probe, limit: limit, max: max})
}

// drainBox drains a boxed decoder onto dst under max, then closes it, and says
// what the stream owed: an overflow, a fault, or the payload. The decoder's
// Close reports the sticky fault of a cut stream, and a stream past the
// ceiling is refused for its size whatever its end holds (kitsunium/sdk#1432),
// so a Close fault is judged only when the stream stayed within max.
func drainBox(dst []byte, box *readerBox, max int64) (decoded []byte, overflow bool, err error) {
	decoded, overflow, err = appendAllBounded(dst, box.rc, &box.probe, max)
	cerr := box.rc.Close()
	//: past the ceiling: the size is the answer, and the decoder's Close fault is not judged.
	if overflow {
		//: no plaintext escapes an over-cap stream.
		return dst, true, nil
	}
	//: a clean drain followed by a Close fault still fails decompression.
	if err == nil {
		err = cerr
	}
	return decoded, false, err
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

// boundedRead is one appendAllBounded call's read: the source, the byte its end
// is probed with, and the two widths of the bound.
type boundedRead struct {
	// r is the decoder being drained.
	r io.Reader
	// probe is the one byte the end of the stream is asked into.
	probe *[1]byte
	// limit is max plus the detector byte, saturated at math.MaxInt64.
	limit int64
	// max is the plaintext the caller agreed to hold.
	max int64
}

// spill reads the rest of the stream into pooled chunks, then grows out once to
// its final length and copies them in. A dst that held the payload exactly
// finds the end with one byte asked into the probe, and is returned as it is,
// unreallocated, having taken no chunk.
func spill(dst, out []byte, br *boundedRead) (decoded []byte, overflow bool, err error) {
	start := len(dst)
	held := int64(len(out) - start)
	//: the end of the stream, asked a byte at a time before any chunk is taken.
	carried, ended, perr := probeEnd(br.r, br.probe, held < br.limit)
	//: a fault while asking: dst goes back as it came, never a partial payload.
	if perr != nil {
		//: the byte that came beside the fault counts: it is the detector when dst
		//: already held max.
		if carried {
			held++
		}
		overflow, perr = ceilingFirst(perr, held, br.max)
		return dst, overflow, perr
	}
	//: the stream is over and dst holds all of it: nothing to spill.
	if ended {
		//: within the cap unless the detector byte is in.
		decoded, overflow = settle(dst, out, br.max)
		return decoded, overflow, nil
	}
	//: the chunk list lives on this frame for the first spillKeepChunks.
	chunks := make([]*spillChunk, 0, spillKeepChunks)
	spilled := 0
	//: the byte the probe found is the first of the first chunk.
	if carried {
		chunks = append(chunks, spillPool.Get())
		chunks[0][0] = br.probe[0]
		spilled = 1
	}
	chunks, spilled, overflow, err = fillChunks(br, chunks, spilled, held)
	//: past the cap, or a fault: the chunks go back, dst as it came.
	if overflow || err != nil {
		//: never a partial payload; the ceiling first if the fault came past it.
		releaseSpill(chunks)
		overflow, err = ceilingFirst(err, held+int64(spilled), br.max)
		return dst, overflow, err
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
	decoded, overflow = settle(dst, out, br.max)
	return decoded, overflow, nil
}

// fillChunks reads the stream into chunks, which already hold spilled bytes,
// until it ends, the limit is reached or it faults, held being the bytes dst
// already holds past its prefix. It hands back the chunks and the bytes they
// hold, and reports overflow when the detector byte is in, and the fault
// verbatim. The list goes in and comes back rather than living in a struct
// beside the caller's frame, which would move its backing array to the heap.
func fillChunks(br *boundedRead, chunks []*spillChunk, spilled int, held int64) (filled []*spillChunk, total int, overflow bool, err error) {
	//: read chunk by chunk until the stream ends or the limit is reached.
	for {
		got := held + int64(spilled)
		//: the detector byte is in: more than max plaintext.
		if got > br.max {
			//: the caller returns dst, never a partial payload.
			return chunks, spilled, true, nil
		}
		//: a full chunk, or none yet: take the next one.
		if spilled == len(chunks)*spillChunkBytes {
			chunks = append(chunks, spillPool.Get())
		}
		room := chunks[len(chunks)-1][spilled%spillChunkBytes:]
		//: never read past the detector byte.
		if left := br.limit - got; int64(len(room)) > left {
			//: left is smaller than a chunk here, so it fits an int.
			room = room[:left]
		}
		n, rerr := br.r.Read(room)
		spilled += n
		//: the stream is over: assemble.
		if rerr == io.EOF {
			//: the end, nothing more to read.
			return chunks, spilled, false, nil
		}
		//: a corrupt stream or a transport fault.
		if rerr != nil {
			//: the fault verbatim.
			return chunks, spilled, false, rerr
		}
	}
}

// ceilingFirst orders the two refusals a read can owe (kitsunium/sdk#1432):
// read is every byte the stream has handed over past dst's prefix, fault the
// one that came with them. Past max the answer is the ceiling's — overflow, no
// fault — because the plaintext a caller refused to hold is a size fact
// whatever the rest of the stream holds; within max the fault stands.
func ceilingFirst(fault error, read, max int64) (overflow bool, err error) {
	//: more than max plaintext came out: the ceiling answers, not the cut.
	if read > max {
		//: nothing is malformed that the caller could have judged.
		return true, nil
	}
	return false, fault
}

// probeEnd asks r for one byte into probe, and says what came back: carried,
// that probe[0] is the stream's next byte (a fault beside it is returned with
// it); ended, that the stream is over with nothing more to take; or a fault. A decoder that answers with no byte and no
// error is asked again, as io.Reader permits. When room is false — the limit is
// reached, so the detector byte is already in — nothing is asked: the loop in
// spill judges the overflow.
func probeEnd(r io.Reader, probe *[1]byte, room bool) (carried, ended bool, err error) {
	//: the limit is reached; there is nothing to ask.
	if !room {
		//: neither a byte nor an end: the chunk loop settles it.
		return false, false, nil
	}
	//: a reader may answer nothing, nil: ask again.
	for {
		n, rerr := r.Read(probe[:])
		//: a byte: it opens the first chunk, even beside the end.
		if n > 0 {
			//: a fault beside a byte is the fault, and the byte is carried with it:
			//: spill decides whether the byte put the stream past the ceiling.
			if rerr != nil && rerr != io.EOF {
				//: surfaced verbatim.
				return true, false, rerr
			}
			//: the byte is carried; the end, if it came, is found again by the loop.
			return true, false, nil
		}
		//: no byte, and the stream is over.
		if rerr == io.EOF {
			//: the end, with nothing to carry.
			return false, true, nil
		}
		//: a corrupt stream or a transport fault.
		if rerr != nil {
			//: surfaced verbatim.
			return false, false, rerr
		}
	}
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
