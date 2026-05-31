// Package streamaead (writer.go) — the streaming-AEAD write path: lazy header
// emit, fixed-size chunk buffering, and per-chunk seal on overflow / Close.
package streamaead

import (
	"io"

	corecrypto "github.com/kitsunium/sdk/internal/core/crypto"
)

const (
	// writerHeaderDone marks that the header has been written to dst.
	writerHeaderDone uint8 = 1 << iota
	// writerClosed marks that Close has already sealed the final chunk.
	writerClosed
)

// streamWriter is the io.WriteCloser returned by streamAEAD.Writer. It buffers
// plaintext into fixed 64 KiB chunks, seals each non-final chunk on overflow,
// and seals the final (possibly short, possibly empty) chunk on Close.
type streamWriter struct {
	// dst is the sink the header and sealed chunks are written to.
	dst io.Writer
	// gcm is the AES-256-GCM mode keyed by the HKDF-derived per-stream key.
	gcm interface {
		Seal(dst, nonce, plaintext, aad []byte) []byte
	}
	// aad is the associated data bound into every chunk's tag.
	aad []byte
	// buf holds plaintext not yet sealed; it never exceeds chunkSize.
	buf []byte
	// counter is the per-chunk nonce counter, incremented after each seal.
	counter uint64
	// salt is the random per-stream HKDF salt written into the header.
	salt [saltLen]byte
	// state packs the headerDone / closed lifecycle bits.
	state uint8
}

// newWriterWithSalt constructs a streamWriter with a caller-supplied salt. It is
// the shared constructor for the production Writer (random salt) and the
// test-only deterministic-salt seam; it stays UNEXPORTED so no consumer can pin
// a salt in production.
func newWriterWithSalt(key corecrypto.Key, dst io.Writer, aad []byte, salt [saltLen]byte) (sealed io.WriteCloser, err error) {
	//: derive the per-stream GCM mode up front; an entropy-free, total operation.
	gcm, gerr := newGCM(key.Bytes(), salt[:])
	//: a derivation/cipher fault is non-oracle (unreachable on the write path).
	if gerr != nil {
		//: surface it rather than panic; the key is always well-formed here.
		return nil, gerr
	}
	//: pre-size the plaintext buffer to one full chunk to avoid regrowth.
	return &streamWriter{dst: dst, gcm: gcm, aad: aad, buf: make([]byte, 0, chunkSize), salt: salt}, nil
}

// Write buffers p into fixed-size chunks, sealing each full non-final chunk as
// it fills. It writes the header lazily on the first call. After Close it
// returns ErrClosedPipe.
func (w *streamWriter) Write(p []byte) (n int, err error) {
	//: a write after Close is a caller error; never seal past the final chunk.
	if w.state&writerClosed != 0 {
		//: the stdlib sentinel for "wrote to a closed stream".
		return 0, io.ErrClosedPipe
	}
	//: emit the header exactly once, before any chunk.
	if herr := w.ensureHeader(); herr != nil {
		//: a header write fault aborts the whole stream.
		return 0, herr
	}
	//: consume p chunk-aligned, sealing whenever the buffer reaches chunkSize.
	for len(p) > 0 {
		//: take only enough to top the current buffer up to one full chunk.
		take := min(chunkSize-len(w.buf), len(p))
		//: stage the slice into the chunk buffer.
		w.buf = append(w.buf, p[:take]...)
		//: account for the consumed input and advance.
		n += take
		p = p[take:]
		//: a full buffer is a non-final chunk; seal it and reset.
		if len(w.buf) == chunkSize {
			//: a non-final chunk carries the more-flag in its nonce.
			if serr := w.sealChunk(flagMore); serr != nil {
				//: propagate the seal/write fault to the caller.
				return n, serr
			}
		}
	}
	//: all of p staged or sealed.
	return n, nil
}

// Close seals the final (possibly short, possibly empty) chunk with the final
// flag and writes it. It is idempotent: a second Close is a no-op.
func (w *streamWriter) Close() error {
	//: a second Close must not seal another (empty) final chunk.
	if w.state&writerClosed != 0 {
		//: idempotent no-op.
		return nil
	}
	//: even an empty stream emits a header + one final-flag chunk.
	if herr := w.ensureHeader(); herr != nil {
		//: a header write fault still marks the writer closed below.
		w.state |= writerClosed
		//: surface the header fault to the caller.
		return herr
	}
	//: mark closed before the final seal so a fault cannot reopen the stream.
	w.state |= writerClosed
	//: the buffered remainder (0..chunkSize) is the final chunk.
	return w.sealChunk(flagFinal)
}

// writeAll writes b to dst in full, returning io.ErrShortWrite when the sink
// accepts fewer than len(b) bytes without reporting an error. A conformant
// io.Writer never short-writes with a nil error, but the frozen stream framing
// cannot tolerate even a non-conformant sink silently truncating the header or a
// sealed chunk — so the byte count is checked explicitly.
func writeAll(dst io.Writer, b []byte) error {
	//: a sink fault is surfaced unchanged; n is meaningless on error.
	n, err := dst.Write(b)
	//: forward a genuine sink fault.
	if err != nil {
		//: the caller aborts the stream on this error.
		return err
	}
	//: a short write with no error violates io.Writer — reject it explicitly.
	if n != len(b) {
		//: the stdlib sentinel for an under-length write.
		return io.ErrShortWrite
	}
	//: the full buffer reached the sink.
	return nil
}

// ensureHeader writes the [version][algID][salt] header to dst exactly once.
func (w *streamWriter) ensureHeader() error {
	//: skip when the header has already been emitted.
	if w.state&writerHeaderDone != 0 {
		//: nothing to do.
		return nil
	}
	//: assemble the fixed header: [version][algID][salt].
	header := make([]byte, 0, headerLen)
	header = append(header, streamVersion, algID)
	header = append(header, w.salt[:]...)
	//: write it in full; a short/failed write aborts the stream.
	if err := writeAll(w.dst, header); err != nil {
		//: surface the sink fault unchanged.
		return err
	}
	//: latch so the header is never re-emitted.
	w.state |= writerHeaderDone
	//: header emitted; chunks may now follow.
	return nil
}

// sealChunk seals the buffered plaintext under the next counter nonce with flag,
// writes ct||tag to dst, advances the counter, and clears the buffer.
func (w *streamWriter) sealChunk(flag byte) error {
	//: build the nonce for this chunk; a uint64 counter can never overflow it.
	nonce := chunkNonce(w.counter, flag)
	//: seal ct||tag onto a fresh slice; aad is authenticated, not stored.
	wire := w.gcm.Seal(nil, nonce[:], w.buf, w.aad)
	//: write the sealed chunk in full; a sink fault aborts the stream.
	if err := writeAll(w.dst, wire); err != nil {
		//: surface the fault unchanged.
		return err
	}
	//: advance the per-chunk counter and reset the buffer for the next chunk.
	w.counter++
	w.buf = w.buf[:0]
	//: chunk sealed and written.
	return nil
}
