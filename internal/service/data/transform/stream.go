package transform

import (
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/concur/recycler"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// resetter is what the three stdlib writers share: a stream encoder that a
// pool can rebind to another destination.
type resetter interface {
	io.WriteCloser
	// Reset rebinds the writer to w, keeping its level.
	Reset(w io.Writer)
}

// poolStream is the writer all three schemes' NewWriter return (ADR 0213): a
// pooled encoder writing into the caller's w until Close, which writes the
// stream's end and returns the encoder to its level's pool, once.
type poolStream[W resetter] struct {
	// w is the pooled encoder; closed says it went back.
	w      W
	closed bool
	// pool is the level's pool w goes back to.
	pool *recycler.Pool[W]
	// wrap is the scheme's WrapParams, for an encoder's fault.
	wrap *errs.WrapParams
}

// Write implements io.Writer: it compresses p into the caller's w.
func (s *poolStream[W]) Write(p []byte) (n int, err error) {
	//: closed: the encoder is back in its pool and must not be touched.
	if s.closed {
		return 0, errs.Wrap(io.ErrClosedPipe, *s.wrap)
	}
	n, werr := s.w.Write(p)
	//: the caller's writer failed, or the encoder did.
	if werr != nil {
		return n, errs.Wrap(werr, *s.wrap)
	}
	return n, nil
}

// Close writes the stream's end into the caller's w and returns the encoder to
// its pool, unbound from w. It does not close w; a second Close is a no-op.
func (s *poolStream[W]) Close() error {
	//: already closed.
	if s.closed {
		return nil
	}
	s.closed = true
	cerr := s.w.Close()
	//: unbind the caller's writer before the pool takes the encoder.
	s.w.Reset(io.Discard)
	s.pool.Put(s.w)
	//: the end did not reach w: the stream is incomplete.
	if cerr != nil {
		return errs.Wrap(cerr, *s.wrap)
	}
	return nil
}

// NewReader implements core/data/transform.StreamingCompressor for gzip: r's
// gzip stream, decoded under max. The header is read here, so a stream that is
// not gzip fails now with GzipFailed.
func (gzipCompressor) NewReader(r io.Reader, max int64) (rc io.ReadCloser, err error) {
	box, terr := takeGzipStream(r)
	//: not a gzip stream, or the caller's reader failed at once.
	if terr != nil {
		return nil, errs.Wrap(terr, gzipWrap)
	}
	return newBoundedStream(box, releaseGzipReader, &gzipWrap, max), nil
}

// NewWriter implements core/data/transform.StreamingCompressor for gzip: a
// gzip stream into w at the receiver's level.
func (c gzipCompressor) NewWriter(w io.Writer) (wc io.WriteCloser, err error) {
	level := writeLevel(c.level)
	enc, terr := takeGzipWriter(w, level)
	//: a level the stdlib refuses: unreachable through the constructor.
	if terr != nil {
		return nil, errs.Wrap(terr, gzipWrap)
	}
	return &poolStream[*gzip.Writer]{w: enc, pool: gzipWriterPoolFor(level), wrap: &gzipWrap}, nil
}

// NewReader implements core/data/transform.StreamingCompressor for raw
// DEFLATE: r's stream, decoded under max. Raw DEFLATE has no header, so a
// stream that is not one fails on its first read.
func (flateCompressor) NewReader(r io.Reader, max int64) (rc io.ReadCloser, err error) {
	box, terr := takeFlateStream(r)
	//: unreachable with today's stdlib, kept typed.
	if terr != nil {
		return nil, errs.Wrap(terr, flateWrap)
	}
	return newBoundedStream(box, releaseFlateReader, &flateWrap, max), nil
}

// NewWriter implements core/data/transform.StreamingCompressor for raw
// DEFLATE: a stream into w at the receiver's level.
func (c flateCompressor) NewWriter(w io.Writer) (wc io.WriteCloser, err error) {
	level := writeLevel(c.level)
	enc, terr := takeFlateWriter(w, level)
	//: a level the stdlib refuses: unreachable through the constructor.
	if terr != nil {
		return nil, errs.Wrap(terr, flateWrap)
	}
	return &poolStream[*flate.Writer]{w: enc, pool: flateWriterPoolFor(level), wrap: &flateWrap}, nil
}

// NewReader implements core/data/transform.StreamingCompressor for zlib: r's
// RFC 1950 stream, decoded under max. The two-byte header is read here, so a
// stream that is not zlib fails now with ZlibFailed.
func (zlibCompressor) NewReader(r io.Reader, max int64) (rc io.ReadCloser, err error) {
	box, terr := takeZlibStream(r)
	//: not a zlib envelope, or the caller's reader failed at once.
	if terr != nil {
		return nil, errs.Wrap(terr, zlibWrap)
	}
	return newBoundedStream(box, releaseZlibReader, &zlibWrap, max), nil
}

// NewWriter implements core/data/transform.StreamingCompressor for zlib: an
// RFC 1950 stream into w at the receiver's level.
func (c zlibCompressor) NewWriter(w io.Writer) (wc io.WriteCloser, err error) {
	enc, terr := takeZlibWriter(w, c.level)
	//: a level the stdlib refuses: unreachable through the constructor.
	if terr != nil {
		return nil, errs.Wrap(terr, zlibWrap)
	}
	return &poolStream[*zlib.Writer]{w: enc, pool: zlibWriterPoolFor(c.level), wrap: &zlibWrap}, nil
}
