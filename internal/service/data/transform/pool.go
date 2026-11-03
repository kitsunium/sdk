package transform

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
	"github.com/kitsunium/sdk/internal/kernel/errs"
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
	// gzipWriterPools recycles *gzip.Writer per level, since NewGzipCompressor
	// gave gzip a level (kitsunium/sdk#900). The level keys the pool for the
	// reason zlibWriterPools states: gzip.Writer.Reset preserves it too.
	gzipWriterPools = newLevelPools[*gzip.Writer]()

	// flateWriterPools recycles *flate.Writer per level, since
	// NewFlateCompressor (kitsunium/sdk#900); flate.Writer.Reset keeps the
	// level the writer was built with, like the other two.
	flateWriterPools = newLevelPools[*flate.Writer]()

	// zlibWriterPools recycles *zlib.Writer per level. The level must key the pool
	// because zlib.Writer.Reset deliberately preserves it: a writer recycled into a
	// differently-levelled compressor would silently encode at the wrong ratio.
	zlibWriterPools = newLevelPools[*zlib.Writer]()

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
	// in is the reader over the caller's compressed input that rc decodes
	// from. It lives in the box, rebound by each take, because a fresh
	// bytes.NewReader per call escapes into the decoder and was one heap
	// allocation on every Decompress — the last one a sized dst could not
	// shed (kitsunium/sdk#1102). It holds the caller's input until the next
	// take, which the decoder did already.
	in bytes.Reader
}

// boundedStream is the reader all three schemes' NewReader return (ADR 0213):
// a pooled decoder read under the caller's ceiling. It hands over at most max
// bytes; once they are out, one more byte is asked of the decoder, and getting
// one fails the read with DecompressedTooLarge where getting the end returns
// io.EOF — the probe is the stream form of appendAllBounded's detector byte.
// Every error is sticky, and Close returns the decoder to its pool once.
type boundedStream struct {
	// box holds the pooled decoder, nil once Close has handed it back.
	box *readerBox
	// release returns box to the pool it was taken from.
	release func(*readerBox)
	// wrap is the scheme's WrapParams, for a decoder's fault.
	wrap *errs.WrapParams
	// left is how many bytes the stream may still hand over.
	left int64
	// err is the sticky outcome: io.EOF, DecompressedTooLarge or a fault.
	err error
	// probe is the one byte asked of the decoder at the ceiling. A field, so
	// the probe does not allocate: the struct is on the heap already.
	probe [1]byte
}

// newBoundedStream wraps a decoder box under ceiling, a negative ceiling read
// as zero, as DecompressBounded reads it.
func newBoundedStream(box *readerBox, release func(*readerBox), wrap *errs.WrapParams, ceiling int64) *boundedStream {
	//: zero admits an empty stream; below zero there is nothing to admit.
	return &boundedStream{box: box, release: release, wrap: wrap, left: max(ceiling, 0)}
}

// Read implements io.Reader under the ceiling.
func (s *boundedStream) Read(p []byte) (n int, err error) {
	//: an outcome is final: the end, the ceiling or a fault.
	if s.err != nil {
		return 0, s.err
	}
	//: closed: the decoder is back in its pool and must not be touched.
	if s.box == nil {
		return 0, errs.Wrap(io.ErrClosedPipe, *s.wrap)
	}
	//: nothing asked, nothing read.
	if len(p) == 0 {
		return 0, nil
	}
	//: the ceiling is reached: the next byte decides.
	if s.left == 0 {
		return 0, s.atCeiling()
	}
	//: never more than the ceiling allows.
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, rerr := s.box.rc.Read(p)
	s.left -= int64(n)
	return n, s.settle(rerr)
}

// atCeiling asks the decoder for one byte past the ceiling: a byte is a
// stream that wanted more than max, the end is a stream that fitted it.
func (s *boundedStream) atCeiling() error {
	n, rerr := s.box.rc.Read(s.probe[:])
	//: a byte past max: refused, and it is never handed over.
	if n > 0 {
		s.err = coretransform.DecompressedTooLarge
		return s.err
	}
	//: the end, a fault, or a decoder that returned nothing yet.
	return s.settle(rerr)
}

// settle turns a decoder's error into the stream's: io.EOF as is, a fault
// under the scheme's sentinel, both sticky.
func (s *boundedStream) settle(rerr error) error {
	//: a read that made progress, or none yet.
	if rerr == nil {
		return nil
	}
	//: the stream ended within the ceiling, its trailer checked.
	if rerr == io.EOF {
		s.err = io.EOF
		return io.EOF
	}
	//: a corrupt stream or the caller's reader failing.
	s.err = errs.Wrap(rerr, *s.wrap)
	return s.err
}

// Close returns the decoder to its pool, once. It does not close the caller's
// reader; a second Close is a no-op.
func (s *boundedStream) Close() error {
	//: already closed.
	if s.box == nil {
		return nil
	}
	box := s.box
	s.box = nil
	//: a decoder's own Close reports what its reads already reported.
	cerr := box.rc.Close()
	s.release(box)
	//: a stream left mid-way is not an error to close; a fault is.
	if cerr != nil && cerr != io.EOF && s.err == nil {
		return errs.Wrap(cerr, *s.wrap)
	}
	return nil
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

// readerBoxPool is the half of a decoder pool a take uses: Get never yields nil
// (a miss is a fresh, empty box) and Put hands a box back. *recycler.Pool
// satisfies it; the interface exists so takeFlateReaderFrom's Reset-fault branch
// can be gated on a pool whose Get returns what Put stored. A sync.Pool promises
// no such thing — a Put lands in the current P's private slot, which a goroutine
// migrated to another P cannot reach, and any collection may drop it — so a
// test driving that branch through the real pool can miss its own fixture, and
// one it missed stays behind to poison a later take.
type readerBoxPool interface {
	// Get borrows a box, a fresh empty one on a miss.
	Get() *readerBox
	// Put returns box for a later Get.
	Put(box *readerBox)
}

// newLevelPools builds one recycler per level in the stdlib's accepted band
// [HuffmanOnly, BestCompression] — the same band and the same constants for
// compress/flate, compress/gzip and compress/zlib, which re-export flate's
// (pinned by TestTheThreeLevelBandsAreOne). Building the pools costs nothing
// at package load: recycler.NewPool stores the factory and calls it only on a
// miss, so importing this package does not construct thirty-six encoders.
func newLevelPools[W any]() []*recycler.Pool[W] {
	pools := make([]*recycler.Pool[W], 0, flate.BestCompression-flate.HuffmanOnly+1)
	//: the band is contiguous and twelve wide, so a slice covers it without a
	//: hash on the take path and without a lock to fill it lazily.
	for level := flate.HuffmanOnly; level <= flate.BestCompression; level++ {
		//: the factory yields W's zero, a nil pointer, because a writer cannot
		//: be built without its destination; the taker constructs on the miss.
		pools = append(pools, recycler.NewPool(func() W {
			var none W
			return none
		}))
	}
	//: indexed by level-HuffmanOnly, see levelPool.
	return pools
}

// levelPool returns pools' recycler for level, or nil where the level is
// outside the band the stdlib constructors accept. A nil pool is how an
// out-of-range level reaches the stdlib constructor unpooled, which is the
// branch that reports it as a typed error.
func levelPool[W any](pools []*recycler.Pool[W], level int) *recycler.Pool[W] {
	//: an out-of-range level has no pool, so it takes the constructor path.
	if level < flate.HuffmanOnly || level > flate.BestCompression {
		//: nil means "not poolable", never "empty pool".
		return nil
	}
	//: HuffmanOnly is the lowest level, so it is index 0.
	return pools[level-flate.HuffmanOnly]
}

// gzipWriterPoolFor returns the gzip recycler for level, nil out of band.
func gzipWriterPoolFor(level int) *recycler.Pool[*gzip.Writer] {
	//: the shared band rule.
	return levelPool(gzipWriterPools, level)
}

// flateWriterPoolFor returns the flate recycler for level, nil out of band.
func flateWriterPoolFor(level int) *recycler.Pool[*flate.Writer] {
	//: the shared band rule.
	return levelPool(flateWriterPools, level)
}

// zlibWriterPoolFor returns the zlib recycler for level, nil out of band.
func zlibWriterPoolFor(level int) *recycler.Pool[*zlib.Writer] {
	//: the shared band rule.
	return levelPool(zlibWriterPools, level)
}

// takeGzipWriter borrows a gzip writer for level, already bound to dst,
// constructing one only on a pool miss. The factory yields nil rather than a
// writer because a writer cannot be built without its destination: an eager
// factory could only bind io.Discard and would then need a second Reset on
// every miss. An out-of-range level has no pool and goes straight to the
// stdlib constructor, which turns it into a typed error.
func takeGzipWriter(dst io.Writer, level int) (writer *gzip.Writer, err error) {
	pool := gzipWriterPoolFor(level)
	//: nil pool means the level is outside the band the stdlib accepts.
	if pool == nil {
		//: unpooled on purpose — this call is expected to fail.
		return gzip.NewWriterLevel(dst, level)
	}
	w := pool.Get()
	//: a miss yields nil, which is the signal to construct.
	if w == nil {
		//: cold path — build a writer this level's pool will keep.
		return gzip.NewWriterLevel(dst, level)
	}
	//: Reset rebinds the recycled writer and clears any prior error state; it
	//: keeps the level, which is why the level keys the pool.
	w.Reset(dst)
	//: ready to encode into dst.
	return w, nil
}

// releaseGzipWriter returns w to its level's pool, rebound to io.Discard so
// the pool does not pin the buffer this package just handed to the caller.
// level always has a pool here, for the reason releaseZlibWriter states.
func releaseGzipWriter(w *gzip.Writer, level int) {
	//: unbind the caller's output before the pool takes ownership.
	w.Reset(io.Discard)
	//: hand the encoder back for the next Compress at this level.
	gzipWriterPoolFor(level).Put(w)
}

// takeFlateWriter borrows a flate writer for level, already bound to dst,
// constructing one only on a pool miss. The constructor's error is returned
// rather than swallowed, so the scheme keeps surfacing it.
func takeFlateWriter(dst io.Writer, level int) (writer *flate.Writer, err error) {
	pool := flateWriterPoolFor(level)
	//: nil pool means the level is outside the band the stdlib accepts.
	if pool == nil {
		//: unpooled on purpose — this call is expected to fail.
		return flate.NewWriter(dst, level)
	}
	w := pool.Get()
	//: a miss yields nil, which is the signal to construct.
	if w == nil {
		//: cold path — the stdlib decides whether the level is acceptable.
		return flate.NewWriter(dst, level)
	}
	//: Reset rebinds the recycled writer and clears any prior error state.
	w.Reset(dst)
	//: ready to encode into dst.
	return w, nil
}

// releaseFlateWriter returns w to its level's pool, unbound from the caller's
// output for the reason releaseGzipWriter states.
func releaseFlateWriter(w *flate.Writer, level int) {
	//: unbind the caller's output before the pool takes ownership.
	w.Reset(io.Discard)
	//: hand the encoder back for the next Compress at this level.
	flateWriterPoolFor(level).Put(w)
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
// expression levelPool tests to report no pool — zlib's constants are flate's. So the two conditions are one condition, and a nil pool here would mean
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

// takeGzipReader borrows a gzip decoder already reading src, through the box's
// own reader. Both the miss path and the reuse path validate the gzip header,
// so a malformed stream is rejected identically either way — the error is the
// caller's to wrap.
func takeGzipReader(src []byte) (box *readerBox, err error) {
	held := gzipReaderPool.Get()
	//: rebind the box's reader to this call's input; no allocation.
	held.in.Reset(src)
	//: the decoder reads the box's own reader.
	return fillGzipBox(held, &held.in)
}

// takeGzipStream borrows a gzip decoder already reading the caller's stream r,
// for a StreamingCompressor reader (ADR 0213). The box's own reader is emptied
// so it does not hold a previous call's input.
func takeGzipStream(r io.Reader) (box *readerBox, err error) {
	held := gzipReaderPool.Get()
	//: the box reads r, not its own reader.
	held.in.Reset(nil)
	return fillGzipBox(held, r)
}

// fillGzipBox points held's decoder at src — the pooled one when held carries
// a gzip decoder, a new one otherwise — and returns held, or puts it back and
// returns the stream's header fault.
func fillGzipBox(held *readerBox, src io.Reader) (box *readerBox, err error) {
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
func takeFlateReader(src []byte) (box *readerBox, err error) {
	//: the process-wide pool; takeFlateReaderFrom holds the logic.
	return takeFlateReaderFrom(flateReaderPool, src)
}

// takeFlateReaderFrom is takeFlateReader over an explicit pool, which is the
// seam that lets the Reset-fault branch be gated deterministically: today's
// stdlib cannot reach it, so its gate has to inject a failing decoder, and only
// a pool whose Get returns what Put stored makes that injection certain.
func takeFlateReaderFrom(pool readerBoxPool, src []byte) (box *readerBox, err error) {
	held := pool.Get()
	//: rebind the box's reader to this call's input; no allocation.
	held.in.Reset(src)
	//: the decoder reads the box's own reader.
	return fillFlateBox(pool, held, &held.in)
}

// takeFlateStream borrows a raw-DEFLATE decoder already reading the caller's
// stream r, for a StreamingCompressor reader (ADR 0213).
func takeFlateStream(r io.Reader) (box *readerBox, err error) {
	held := flateReaderPool.Get()
	//: the box reads r, not its own reader.
	held.in.Reset(nil)
	return fillFlateBox(flateReaderPool, held, r)
}

// fillFlateBox points held's decoder at src, as fillGzipBox does for gzip.
func fillFlateBox(pool readerBoxPool, held *readerBox, src io.Reader) (box *readerBox, err error) {
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
		pool.Put(held)
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

// takeZlibReader borrows a zlib decoder already reading src, through the box's
// own reader. Both paths parse the RFC 1950 header, so a bad envelope is
// rejected identically either way.
func takeZlibReader(src []byte) (box *readerBox, err error) {
	held := zlibReaderPool.Get()
	//: rebind the box's reader to this call's input; no allocation.
	held.in.Reset(src)
	//: the decoder reads the box's own reader.
	return fillZlibBox(held, &held.in)
}

// takeZlibStream borrows a zlib decoder already reading the caller's stream r,
// for a StreamingCompressor reader (ADR 0213).
func takeZlibStream(r io.Reader) (box *readerBox, err error) {
	held := zlibReaderPool.Get()
	//: the box reads r, not its own reader.
	held.in.Reset(nil)
	return fillZlibBox(held, r)
}

// fillZlibBox points held's decoder at src, as fillGzipBox does for gzip.
func fillZlibBox(held *readerBox, src io.Reader) (box *readerBox, err error) {
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
