package transform

import (
	"io"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/zstd"

	coretransform "github.com/kitsunium/sdk/internal/core/data/transform"
	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// releaseZstdDecoder unbinds a stream decoder from the caller's reader and
// hands it back to pool. A decoder that cannot be reset — one closed — is
// dropped rather than pooled.
func releaseZstdDecoder(pool *recycler.Pool[*zstd.Decoder], dec *zstd.Decoder) {
	//: a nil reader drops the caller's; only a closed decoder refuses it.
	if dec.Reset(nil) != nil {
		return
	}
	pool.Put(dec)
}

// releaseS2Reader unbinds an s2 stream reader from the caller's reader and
// hands it back to the shared pool.
func releaseS2Reader(dec *s2.Reader) {
	dec.Reset(nil)
	s2ReaderPool.Put(dec)
}

// s2ReaderPool and s2WriterPool recycle the s2 stream codecs: every
// S2Compressor builds them with the same options, so one pool serves all, and
// a stream costs no 1 MiB buffer per call once the pool is warm.
var (
	s2ReaderPool = recycler.NewPool(func() *s2.Reader { return nil })
	s2WriterPool = recycler.NewPool(func() *s2.Writer { return nil })
)

// boundedStream is the reader NewReader returns for both schemes (ADR 0213):
// the library's stream decoder read under the caller's ceiling. It hands over
// at most limit bytes; once they are out, one more byte is asked of the decoder,
// and getting one fails the read with core/data/transform.DecompressedTooLarge
// where getting the end returns io.EOF. Every error is sticky; a library error
// is classified as the block path classifies it, so a window past the
// compressor's ceiling is DecompressionLimitExceeded and a frame naming an
// unknown dictionary is DictionaryMismatch.
type boundedStream struct {
	// dec is the decoder, nil once Close has released it.
	dec io.Reader
	// release hands dec back to its pool; Close calls it once.
	release func()
	// classify turns a library error into the scheme's.
	classify func(error) error
	// closed is the scheme's refusal of a read after Close.
	closed *errs.WrapParams
	// left is how many bytes the stream may still hand over.
	left int64
	// err is the sticky outcome: io.EOF, DecompressedTooLarge or a fault.
	err error
	// probe is the one byte asked of the decoder at the ceiling.
	probe [1]byte
}

// Read implements io.Reader under the ceiling.
func (s *boundedStream) Read(p []byte) (n int, err error) {
	//: an outcome is final: the end, the ceiling or a fault.
	if s.err != nil {
		return 0, s.err
	}
	//: closed: the decoder is released and must not be touched.
	if s.dec == nil {
		return 0, errs.Wrap(io.ErrClosedPipe, *s.closed)
	}
	//: nothing asked, nothing read.
	if len(p) == 0 {
		return 0, nil
	}
	//: the ceiling is reached: the next byte decides.
	if s.left == 0 {
		probed, rerr := s.dec.Read(s.probe[:])
		//: a byte past max: refused, and never handed over.
		if probed > 0 {
			s.err = coretransform.DecompressedTooLarge
			return 0, s.err
		}
		return 0, s.settle(rerr)
	}
	//: never more than the ceiling allows.
	if int64(len(p)) > s.left {
		p = p[:s.left]
	}
	n, rerr := s.dec.Read(p)
	s.left -= int64(n)
	return n, s.settle(rerr)
}

// settle turns the decoder's error into the stream's, sticky.
func (s *boundedStream) settle(rerr error) error {
	//: a read that made progress, or none yet.
	if rerr == nil {
		return nil
	}
	//: the stream ended within the ceiling, its checksums verified.
	if rerr == io.EOF {
		s.err = io.EOF
		return io.EOF
	}
	//: a corrupt stream, a refusal of the library's, the caller's reader.
	s.err = s.classify(rerr)
	return s.err
}

// Close releases the decoder, once. It does not close the caller's reader; a
// second Close is a no-op.
func (s *boundedStream) Close() error {
	//: already closed.
	if s.dec == nil {
		return nil
	}
	s.release()
	s.dec = nil
	return nil
}

// encoderStream is the writer NewWriter returns for both schemes: the
// library's encoder, its errors under the scheme's sentinel, a write after
// Close refused, and the encoder handed back to its pool by Close.
type encoderStream struct {
	enc    io.WriteCloser
	closed bool
	wrap   *errs.WrapParams
	// release unbinds the encoder from the caller's writer and pools it.
	release func()
}

// Write implements io.Writer: it compresses p into the caller's w.
func (s *encoderStream) Write(p []byte) (n int, err error) {
	//: closed: the stream has ended.
	if s.closed {
		return 0, errs.Wrap(io.ErrClosedPipe, *s.wrap)
	}
	n, werr := s.enc.Write(p)
	//: the caller's writer failed, or the encoder did.
	if werr != nil {
		return n, errs.Wrap(werr, *s.wrap)
	}
	return n, nil
}

// Close writes the stream's end into the caller's w and releases the encoder.
// It does not close w; a second Close is a no-op.
func (s *encoderStream) Close() error {
	//: already closed.
	if s.closed {
		return nil
	}
	s.closed = true
	cerr := s.enc.Close()
	s.release()
	//: the end did not reach w: the stream is incomplete.
	if cerr != nil {
		return errs.Wrap(cerr, *s.wrap)
	}
	return nil
}

// NewReader implements core/data/transform.StreamingCompressor for zstd: r's
// stream of zstd frames, decoded under limit, a negative limit read as zero.
//
// The decoder runs synchronously in the caller's goroutine
// (WithDecoderConcurrency(1)): a stream spawns no goroutine. Its memory is its
// window, which WithDecoderMaxMemory bounds by the compressor's own ceiling —
// a frame declaring a larger one is refused with DecompressionLimitExceeded at
// its header — while limit bounds what the caller receives. A compressor built
// with a dictionary reads the frames that name it, and a frame naming another
// is DictionaryMismatch.
func (z *ZstdCompressor) NewReader(r io.Reader, limit int64) (rc io.ReadCloser, err error) {
	dec, derr := z.takeStreamDecoder(r)
	//: an option fault, or the caller's reader failing on the first frame.
	if derr != nil {
		return nil, z.classifyStream(derr)
	}
	return &boundedStream{
		dec: dec, release: func() { releaseZstdDecoder(z.streamDecoders, dec) },
		classify: z.classifyStream, closed: &zstdWrap, left: max(0, limit),
	}, nil
}

// takeStreamDecoder borrows a stream decoder bound to r from the compressor's
// pool, or builds one — synchronous, bounded by the ceiling, with the
// dictionary — on a miss.
func (z *ZstdCompressor) takeStreamDecoder(r io.Reader) (dec *zstd.Decoder, err error) {
	dec = z.streamDecoders.Get()
	//: a miss: build one with the compressor's options.
	if dec == nil {
		opts := []zstd.DOption{
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderMaxMemory(uint64(z.maxDecompressedBytes)),
		}
		//: the dictionary rides on the stream's decoder too.
		if z.dict != nil {
			opts = append(opts, zstd.WithDecoderDicts(z.dict))
		}
		return zstd.NewReader(r, opts...)
	}
	//: a hit: rebind it to r.
	if rerr := dec.Reset(r); rerr != nil {
		releaseZstdDecoder(z.streamDecoders, dec)
		return nil, rerr
	}
	return dec, nil
}

// classifyStream is classify for a stream, which has no source slice to read
// a frame header from: a dictionary mismatch carries the compressor's ID only.
func (z *ZstdCompressor) classifyStream(cause error) error {
	//: the same split as the block path, without the frame's own ID.
	return z.classify(cause, nil)
}

// NewWriter implements core/data/transform.StreamingCompressor for zstd: a
// stream of zstd frames into w, at the compressor's level and with its
// dictionary. The encoder runs synchronously in the caller's goroutine
// (WithEncoderConcurrency(1)). What it writes, Decompress reads too: a zstd
// stream is a sequence of frames, as a block is.
func (z *ZstdCompressor) NewWriter(w io.Writer) (wc io.WriteCloser, err error) {
	enc := z.streamEncoders.Get()
	//: a hit: rebind it to w.
	if enc != nil {
		enc.Reset(w)
	} else {
		opts := []zstd.EOption{zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(z.level)}
		//: the dictionary rides on the stream's encoder too.
		if z.dict != nil {
			opts = append(opts, zstd.WithEncoderDict(z.dict))
		}
		var eerr error
		enc, eerr = zstd.NewWriter(w, opts...)
		//: a library-option fault, unreachable with the options above.
		if eerr != nil {
			return nil, errs.Wrap(eerr, zstdWrap)
		}
	}
	return &encoderStream{enc: enc, wrap: &zstdWrap, release: func() {
		//: unbind the caller's writer before the pool keeps the encoder.
		enc.Reset(io.Discard)
		z.streamEncoders.Put(enc)
	}}, nil
}

// NewReader implements core/data/transform.StreamingCompressor for s2: r's
// stream in the s2 FRAMING format — the one NewWriter writes — decoded under
// limit, a negative limit read as zero. It is not the block format Compress
// writes and Decompress reads: a block declares its length in a header and has
// no stream form, so an s2 stream is a different wire format, refused here as
// a malformed stream when handed a block, and refused by Decompress the other
// way. Its memory is one stream block, at most the framing format's 4 MiB.
func (s *S2Compressor) NewReader(r io.Reader, limit int64) (rc io.ReadCloser, err error) {
	dec := s2ReaderPool.Get()
	//: a hit is rebound to r, a miss built for it.
	if dec != nil {
		dec.Reset(r)
	} else {
		dec = s2.NewReader(r)
	}
	return &boundedStream{
		dec: dec, release: func() { releaseS2Reader(dec) },
		classify: s.classifyStream, closed: &s2Wrap, left: max(0, limit),
	}, nil
}

// classifyStream turns an s2 stream error into the scheme's: every library
// error of a stream is a malformed one, S2Failed.
func (*S2Compressor) classifyStream(cause error) error {
	//: the library's reason kept beneath the sentinel.
	return errs.Wrap(cause, s2Wrap)
}

// NewWriter implements core/data/transform.StreamingCompressor for s2: a
// stream in the s2 framing format into w, which NewReader reads and
// Decompress does not (see NewReader). The encoder runs synchronously in the
// caller's goroutine (s2.WriterConcurrency(1)).
func (*S2Compressor) NewWriter(w io.Writer) (wc io.WriteCloser, err error) {
	enc := s2WriterPool.Get()
	//: a hit is rebound to w, a miss built for it.
	if enc != nil {
		enc.Reset(w)
	} else {
		enc = s2.NewWriter(w, s2.WriterConcurrency(1))
	}
	return &encoderStream{enc: enc, wrap: &s2Wrap, release: func() {
		//: unbind the caller's writer before the pool keeps the encoder.
		enc.Reset(io.Discard)
		s2WriterPool.Put(enc)
	}}, nil
}
