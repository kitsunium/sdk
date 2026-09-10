// Package transform — the recycled stdlib codecs behind Compress and
// Decompress. Neither direction's stdlib object is small: at the default level
// an encoder carries two hash tables and a 320 KiB history window, and a
// decoder carries a 32 KiB dictionary window. Constructing one per call put
// compress/flate at 99 % of the bytes a 256-byte gzip allocated and
// runtime.memclrNoHeapPointers at 18.7 % of its CPU — zeroing tables the call
// was about to overwrite. Recycling moves that off the per-call path; the
// before/after medians are in BENCH.md.
//
// A pooled object is reachable until the next take on the same P or the next
// GC, whichever comes first, and it is large. That is why the two directions
// unbind differently, and the line is what the SDK created versus what the
// caller lent us: an encoder is rebound to io.Discard before it goes back,
// because it would otherwise pin the output buffer this package built and
// handed to the caller as exclusively theirs; a decoder is not rebound,
// because what it holds is the caller's own compressed input, which the caller
// passed in and still has — and because every decoder Reset re-reads a header
// and so returns an error that an unbind would have to discard, while every
// encoder Reset returns nothing at all.
//
// Every take checks the pooled decoder rather than trusting it, and the check
// is in two parts because one of them cannot do the job alone: a type assertion
// establishes the Reset CAPABILITY, and a tag on the box establishes the
// SCHEME. readerScheme carries the measurement that forces the split — the
// flate and zlib Resetter interfaces declare the identical method, so each
// pool's assertion accepts the other pool's decoder and detects nothing.
//
// A box holding another scheme's decoder is a corrupted pool, which is this
// package's own defect and not the caller's stream: nobody who called
// Decompress could have caused it and nobody could act on being told. So it is
// never an error. It fails the same condition an empty box fails, takes the
// same constructor path, and the fresh decoder overwrites the foreign one — so
// the fault costs exactly the allocation the pool exists to avoid, once, and
// does not outlive the take that found it.
package transform

import (
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/recycler"
)

const (
	// schemeNone tags a box holding no decoder: the state a fresh box leaves
	// the factory in, and the state a take restores when a constructor refuses
	// the caller's stream and the box goes back empty.
	schemeNone readerScheme = iota
	// schemeGzip tags a box filled by takeGzipReader.
	schemeGzip
	// schemeFlate tags a box filled by takeFlateReader.
	schemeFlate
	// schemeZlib tags a box filled by takeZlibReader.
	schemeZlib
)

var (
	// gzipWriterPool recycles *gzip.Writer across Compress calls. gzip encodes at
	// DefaultCompression here and nothing varies it, so one pool covers the scheme.
	gzipWriterPool = recycler.NewPool(func() *gzip.Writer { return nil })

	// flateWriterPool recycles *flate.Writer across Compress calls, at the single
	// DefaultCompression level this scheme encodes at.
	flateWriterPool = recycler.NewPool(func() *flate.Writer { return nil })

	// zlibWriterPools recycles *zlib.Writer per level. The level must key the pool
	// because zlib.Writer.Reset deliberately preserves it: a writer recycled into a
	// differently-levelled compressor would silently encode at the wrong ratio.
	zlibWriterPools = newZlibWriterPools()

	// gzipReaderPool recycles the gzip decoder, held in a box rather than
	// directly for the reason readerBox states.
	gzipReaderPool = recycler.NewPool(func() *readerBox { return &readerBox{} })

	// flateReaderPool recycles the raw-DEFLATE decoder, whose concrete type the
	// stdlib does not export — the box is what makes a miss representable.
	flateReaderPool = recycler.NewPool(func() *readerBox { return &readerBox{} })

	// zlibReaderPool recycles the RFC 1950 decoder, unexported for the same
	// reason and boxed the same way, so the three schemes read identically.
	zlibReaderPool = recycler.NewPool(func() *readerBox { return &readerBox{} })
)

// readerBox carries a recycled decoder, and exists because two of the three
// stdlib decoders have an unexported concrete type: the pool can only hold them
// as io.ReadCloser, and a pool of a nil INTERFACE cannot signal a miss —
// recycler.Pool.Get type-asserts its entry, and a nil interface fails that
// assertion and panics ("recycler: pool yielded unexpected type"), while a nil
// POINTER passes it and reads back as nil. A one-word box makes "no decoder
// yet" representable for all three schemes; gzip alone could have gone in the
// pool unboxed, and does not, so the three schemes read identically.
type readerBox struct {
	// rc is the recycled decoder, nil until the first miss has been served.
	rc io.ReadCloser
	// scheme names which take filled rc, and is the only thing that tells the
	// three pools' decoders apart — see readerScheme for the measurement that
	// forces it. It is written in the same statement that writes rc, never
	// separately, so the two cannot disagree.
	scheme readerScheme
}

// readerScheme names the wire format a boxed decoder was built for, and exists
// because the type assertion each take performs cannot establish it. MEASURED,
// on the Go 1.27 standard library:
//
//	*zlib.reader        satisfies flate.Resetter : true
//	*flate.decompressor satisfies zlib.Resetter  : true
//
// Both interfaces declare the one identical method Reset(io.Reader, []byte)
// error, so each pool's assertion accepts the other pool's decoder and detects
// nothing at all. Only takeGzipReader, which asserts the concrete *gzip.Reader,
// could tell the schemes apart on the type alone; flate.NewReader returns an
// unexported type, so no concrete assertion is available for flate and the
// scheme has to be CARRIED rather than inferred. One field and one comparison
// buy for all three schemes what gzip got for free.
//
// What the absent detection cost, before this tag, was also measured. A zlib
// decoder reached through the flate pool parsed an RFC 1950 header out of a raw
// DEFLATE stream and returned "zlib: invalid header" under FlateFailed — and
// because the assertion had passed, the take handed the foreign decoder
// straight back to the pool, so every later flate Decompress on that P failed
// identically, for the life of the process. The reverse crossing was quieter
// and worse: a flate decoder reached through the zlib pool ACCEPTED the Reset,
// skipping the two-byte header check that is the zlib scheme's whole reason to
// exist, and went on to judge the stream by the wrong grammar.
type readerScheme uint8

// newZlibWriterPools builds one recycler per level in the stdlib's accepted
// band [HuffmanOnly, BestCompression]. Building the pools costs nothing at
// package load: recycler.NewPool stores the factory and calls it only on a
// miss, so importing this package for its gzip registration does not construct
// twelve zlib encoders.
func newZlibWriterPools() []*recycler.Pool[*zlib.Writer] {
	pools := make([]*recycler.Pool[*zlib.Writer], 0, zlib.BestCompression-zlib.HuffmanOnly+1)
	//: the band is contiguous and twelve wide, so a slice covers it without a
	//: hash on the take path and without a lock to fill it lazily.
	for level := zlib.HuffmanOnly; level <= zlib.BestCompression; level++ {
		//: the factory yields nil because a writer cannot be built without its
		//: destination; the taker constructs on the miss instead.
		pools = append(pools, recycler.NewPool(func() *zlib.Writer { return nil }))
	}
	//: indexed by level-HuffmanOnly, see zlibWriterPoolFor.
	return pools
}

// zlibWriterPoolFor returns the recycler for level, or nil where the level is
// outside the band zlib.NewWriterLevel accepts. A nil pool is how the
// out-of-range level reaches the stdlib constructor unpooled, which is the
// branch that reports it as a typed error.
func zlibWriterPoolFor(level int) *recycler.Pool[*zlib.Writer] {
	//: an out-of-range level has no pool, so it takes the constructor path.
	if level < zlib.HuffmanOnly || level > zlib.BestCompression {
		//: nil means "not poolable", never "empty pool".
		return nil
	}
	//: HuffmanOnly is the lowest level, so it is index 0.
	return zlibWriterPools[level-zlib.HuffmanOnly]
}

// takeGzipWriter borrows a gzip writer already bound to dst, constructing one
// only on a pool miss. The factory yields nil rather than a writer because a
// writer cannot be built without its destination: an eager factory could only
// bind io.Discard and would then need a second Reset on every miss.
func takeGzipWriter(dst io.Writer) *gzip.Writer {
	w := gzipWriterPool.Get()
	//: a miss yields nil, which is the signal to construct.
	if w == nil {
		//: cold path — build a writer the pool will keep after this call.
		return gzip.NewWriter(dst)
	}
	//: Reset rebinds the recycled writer and clears any prior error state.
	w.Reset(dst)
	//: ready to encode into dst.
	return w
}

// releaseGzipWriter returns w to the pool, rebound to io.Discard so the pool
// does not pin the buffer this package just handed to the caller.
func releaseGzipWriter(w *gzip.Writer) {
	//: unbind the caller's output before the pool takes ownership.
	w.Reset(io.Discard)
	//: hand the encoder back for the next Compress on this P.
	gzipWriterPool.Put(w)
}

// takeFlateWriter borrows a flate writer already bound to dst, constructing one
// only on a pool miss. The constructor's error is returned rather than
// swallowed, so the scheme keeps surfacing it.
func takeFlateWriter(dst io.Writer) (writer *flate.Writer, err error) {
	w := flateWriterPool.Get()
	//: a miss yields nil, which is the signal to construct.
	if w == nil {
		//: cold path — the stdlib decides whether the level is acceptable.
		return flate.NewWriter(dst, flate.DefaultCompression)
	}
	//: Reset rebinds the recycled writer and clears any prior error state.
	w.Reset(dst)
	//: ready to encode into dst.
	return w, nil
}

// releaseFlateWriter returns w to the pool, unbound from the caller's output
// for the reason releaseGzipWriter states.
func releaseFlateWriter(w *flate.Writer) {
	//: unbind the caller's output before the pool takes ownership.
	w.Reset(io.Discard)
	//: hand the encoder back for the next Compress on this P.
	flateWriterPool.Put(w)
}

// takeZlibWriter borrows a zlib writer for level, already bound to dst. An
// out-of-range level has no pool and goes straight to the stdlib constructor,
// which is what turns it into a typed error.
func takeZlibWriter(dst io.Writer, level int) (writer *zlib.Writer, err error) {
	pool := zlibWriterPoolFor(level)
	//: nil pool means the level is outside the band the stdlib accepts.
	if pool == nil {
		//: unpooled on purpose — this call is expected to fail.
		return zlib.NewWriterLevel(dst, level)
	}
	w := pool.Get()
	//: a miss yields nil, which is the signal to construct.
	if w == nil {
		//: cold path — build a writer this level's pool will keep.
		return zlib.NewWriterLevel(dst, level)
	}
	//: Reset rebinds the recycled writer; it preserves the level by design.
	w.Reset(dst)
	//: ready to encode into dst at the receiver's level.
	return w, nil
}

// releaseZlibWriter returns w to its level's pool, unbound from the caller's
// output for the reason releaseGzipWriter states.
//
// level always has a pool here, and the branch that used to test for one is
// gone rather than left to describe a writer that cannot exist. The chain: this
// function only ever runs from a defer the zlib scheme registers AFTER
// takeZlibWriter returned a nil error; takeZlibWriter's unpooled branch returns
// zlib.NewWriterLevel's own result verbatim; and NewWriterLevel refuses exactly
// `level < HuffmanOnly || level > BestCompression`, which is the same
// expression, character for character, that makes zlibWriterPoolFor report no
// pool. So the two conditions are one condition, and a nil pool here would mean
// a writer the stdlib had refused to build was nonetheless handed back — a
// broken invariant, not a level to handle. It is left to fault, exactly as
// recycler.Pool.Get faults on a wrong-typed entry rather than masking it. The
// equivalence is not asserted at runtime on the hot path; it is pinned across
// the whole int range by TestZlibWriterPoolMatchesStdlibLevelBand.
func releaseZlibWriter(w *zlib.Writer, level int) {
	//: unbind the caller's output before the pool takes ownership.
	w.Reset(io.Discard)
	//: hand the encoder back for the next Compress at this level.
	zlibWriterPoolFor(level).Put(w)
}

// takeGzipReader borrows a gzip decoder already reading src. Both the miss path
// and the reuse path validate the gzip header, so a malformed stream is
// rejected identically either way — the error is the caller's to wrap.
func takeGzipReader(src io.Reader) (box *readerBox, err error) {
	held := gzipReaderPool.Get()
	decoder, pooled := held.rc.(*gzip.Reader)
	//: an empty box (a miss) and a box holding another scheme's decoder (a
	//: corrupted pool) fail this one condition together, and construction is
	//: the right answer to both: it is correct, it costs only the allocation
	//: the pool exists to avoid, and it needs nothing from the caller — who
	//: could neither have caused the second case nor act on being told of it.
	//: gzip is the one scheme whose assertion would have caught the crossing
	//: unaided; the tag is checked anyway so the three schemes read identically.
	if !pooled || held.scheme != schemeGzip {
		//: cold path — NewReader parses the header and reports a bad one.
		r, nerr := gzip.NewReader(src)
		//: a refused header is the caller's stream, and travels back as such.
		if nerr != nil {
			//: empty the box before it goes back: a foreign decoder left in it
			//: would fail the condition again on every later take, turning one
			//: corruption into a pool that never hits.
			held.rc, held.scheme = nil, schemeNone
			gzipReaderPool.Put(held)
			//: header fault — the scheme wraps it under its sentinel.
			return nil, nerr
		}
		//: keep the decoder for the next call on this P; writing rc and scheme
		//: together also overwrites a foreign pair, so the pool heals on the
		//: take that found it and the two fields can never disagree.
		held.rc, held.scheme = r, schemeGzip
		//: ready to drain.
		return held, nil
	}
	//: Reset re-initialises the decoder completely and re-parses the header.
	if rerr := decoder.Reset(src); rerr != nil {
		//: a rejected header leaves a fully-reset decoder of this scheme, so
		//: the box goes back tagged as it already was.
		gzipReaderPool.Put(held)
		//: header fault — the scheme wraps it under its sentinel.
		return nil, rerr
	}
	//: ready to drain.
	return held, nil
}

// releaseGzipReader returns box to the pool still bound to the caller's input,
// which the package-level comment explains.
func releaseGzipReader(box *readerBox) {
	//: hand the decoder back for the next Decompress on this P.
	gzipReaderPool.Put(box)
}

// takeFlateReader borrows a raw-DEFLATE decoder already reading src. Raw
// DEFLATE has no header, so neither path can reject the stream here; the
// Resetter interface is still allowed to fail, and that failure is returned
// typed rather than discarded.
func takeFlateReader(src io.Reader) (box *readerBox, err error) {
	held := flateReaderPool.Get()
	resetter, pooled := held.rc.(flate.Resetter)
	//: two things are being asked and neither answers the other. The assertion
	//: establishes the CAPABILITY: it is to an INTERFACE, and zlib.Resetter
	//: declares the identical method, so a zlib decoder passes it — measured,
	//: see readerScheme. The tag establishes the SCHEME, which is what the
	//: assertion cannot, because flate.NewReader's concrete type is unexported
	//: and there is nothing here to compare it against. An empty box, a foreign
	//: decoder and a decoder that cannot take this Reset fail the one condition
	//: together, and all three want the constructor rather than an error — a
	//: wrongly-filled pool is this package's defect, not the caller's stream.
	if !pooled || held.scheme != schemeFlate {
		//: cold path — flate.NewReader has no header to reject and cannot fail;
		//: writing rc and scheme together also overwrites a foreign pair, so the
		//: pool heals on the take that found it rather than staying wedged.
		held.rc, held.scheme = flate.NewReader(src), schemeFlate
		//: ready to drain.
		return held, nil
	}
	//: no dictionary: this scheme never writes one, so it never reads one.
	if rerr := resetter.Reset(src, nil); rerr != nil {
		//: unreachable with today's stdlib, whose Reset ends in `return nil`;
		//: kept so a future implementation that CAN fail fails typed.
		flateReaderPool.Put(held)
		//: hand the fault back for the scheme to wrap.
		return nil, rerr
	}
	//: ready to drain.
	return held, nil
}

// releaseFlateReader returns box to the pool still bound to the caller's input,
// which the package-level comment explains.
func releaseFlateReader(box *readerBox) {
	//: hand the decoder back for the next Decompress on this P.
	flateReaderPool.Put(box)
}

// takeZlibReader borrows a zlib decoder already reading src. Both paths parse
// the RFC 1950 header, so a bad envelope is rejected identically either way.
func takeZlibReader(src io.Reader) (box *readerBox, err error) {
	held := zlibReaderPool.Get()
	resetter, pooled := held.rc.(zlib.Resetter)
	//: the assertion establishes the CAPABILITY and the tag the SCHEME, for the
	//: reason takeFlateReader states at length: flate.Resetter declares the
	//: identical method, so a raw-DEFLATE decoder passes this assertion and
	//: would then skip the two-byte header check this scheme exists to perform.
	//: An empty box and a foreign decoder fail the one condition together, and
	//: construction answers both — an error here would name a corruption of this
	//: package's own pool to a caller who can do nothing with it.
	if !pooled || held.scheme != schemeZlib {
		//: cold path — NewReader parses the 2-byte header and reports a bad one.
		r, nerr := zlib.NewReader(src)
		//: a refused envelope is the caller's stream, and travels back as such.
		if nerr != nil {
			//: empty the box before it goes back, for the reason takeGzipReader
			//: states: a decoder this scheme cannot use would fail the condition
			//: on every later take.
			held.rc, held.scheme = nil, schemeNone
			zlibReaderPool.Put(held)
			//: header fault — the scheme wraps it under its sentinel.
			return nil, nerr
		}
		//: keep the decoder for the next call on this P; writing rc and scheme
		//: together also overwrites a foreign pair, so the pool heals on the
		//: take that found it and the two fields can never disagree.
		held.rc, held.scheme = r, schemeZlib
		//: ready to drain.
		return held, nil
	}
	//: no dictionary: this scheme never writes one, so it never reads one.
	if rerr := resetter.Reset(src, nil); rerr != nil {
		//: a rejected header leaves a fully-reset decoder of this scheme, so
		//: the box goes back tagged as it already was.
		zlibReaderPool.Put(held)
		//: header fault — the scheme wraps it under its sentinel.
		return nil, rerr
	}
	//: ready to drain.
	return held, nil
}

// releaseZlibReader returns box to the pool still bound to the caller's input,
// which the package-level comment explains.
func releaseZlibReader(box *readerBox) {
	//: hand the decoder back for the next Decompress on this P.
	zlibReaderPool.Put(box)
}
