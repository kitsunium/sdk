package scratch

import "github.com/kitsunium/sdk/internal/kernel/concur/recycler"

// appendWriterPool recycles *AppendWriter. A writer is two words and an int,
// handed to a standard-library encoder as an io.Writer — which moves it to the
// heap on every call; the pool is what keeps an Append at the encoder's own
// allocations. Release clears the slice, so no reset-on-Put is needed.
var appendWriterPool = recycler.NewPool[*AppendWriter](func() *AppendWriter { return new(AppendWriter) })

// AppendWriter is an io.Writer over a caller's slice: each Write appends to
// it, growing it as append grows a slice, and [AppendWriter.Bytes] returns
// the slice so far. [AppendWriter.Truncate] cuts it back to the length it had
// when the writer was acquired, so an encode that fails half-way leaves the
// caller's bytes as they were — the codec.Appender contract. It never fails a
// Write. Acquire one with AcquireAppendWriter for one encode, or keep one from
// NewAppendWriter; the zero AppendWriter appends to a nil slice.
type AppendWriter struct {
	// bytes is the caller's slice followed by everything written since.
	bytes []byte
	// base is len(bytes) when the writer was acquired: what Truncate keeps.
	base int
}

// NewAppendWriter returns an [AppendWriter] appending to dst that is the
// caller's to keep, outside the pool: for a writer that lives as long as its
// owner — a stream encoder writing every record through the same one — where
// AcquireAppendWriter is for one encode.
func NewAppendWriter(dst []byte) *AppendWriter {
	//: a fresh writer over dst.
	return &AppendWriter{bytes: dst, base: len(dst)}
}

// AcquireAppendWriter returns an [AppendWriter] from the shared pool that
// appends to dst in place: what an encoder writes through it lands after
// dst's last byte, in dst's spare capacity while it lasts. It is how a codec's
// Append hands a caller's buffer to a standard-library encoder that takes an
// io.Writer, instead of encoding into a pooled buffer and copying the result
// onto dst. The caller owns the writer until ReleaseAppendWriter.
func AcquireAppendWriter(dst []byte) *AppendWriter {
	//: a pooled writer, cleared by its previous release.
	w := appendWriterPool.Get()
	//: point it at the caller's slice and remember where it ended.
	w.bytes, w.base = dst, len(dst)
	//: the caller owns it until ReleaseAppendWriter.
	return w
}

// ReleaseAppendWriter returns w to the shared pool. The writer forgets the
// slice it held, so an idle writer keeps no caller's buffer alive. Callers
// MUST NOT use w after this call; the slice [AppendWriter.Bytes] returned
// stays theirs. A nil w is a no-op.
func ReleaseAppendWriter(w *AppendWriter) {
	//: nil-safe, like the other releases.
	if w == nil {
		//: nothing to recycle.
		return
	}
	//: drop the caller's slice before pooling the writer.
	w.bytes, w.base = nil, 0
	//: the next AcquireAppendWriter repoints it.
	appendWriterPool.Put(w)
}

// Write appends p to the slice and reports len(p). It never returns an
// error, so an encoder writing through it fails only on its own account.
func (w *AppendWriter) Write(p []byte) (n int, err error) {
	//: append grows the slice geometrically, as for any caller's slice.
	w.bytes = append(w.bytes, p...)
	//: everything was taken.
	return len(p), nil
}

// Bytes returns the slice so far: the bytes the writer was acquired with,
// then everything written since. It may be a new array when a Write
// outgrew the capacity of the one before.
func (w *AppendWriter) Bytes() []byte {
	//: the caller's prefix and what was written after it.
	return w.bytes
}

// Truncate drops everything written since the writer was acquired and
// returns the slice cut back to its length then, so a failed encode hands
// the caller back the bytes it gave, unchanged.
func (w *AppendWriter) Truncate() []byte {
	//: the prefix is untouched: every Write appended after it.
	w.bytes = w.bytes[:w.base]
	//: the caller's bytes, at their original length.
	return w.bytes
}
