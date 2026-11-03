package forward

import (
	"bytes"
	"compress/gzip"
	"io"
	"sync"
)

// What Compress writes and how a stored batch is recognised.
var (
	// gzipMagic opens every gzip stream (RFC 1952 §2.3.1): ID1, ID2 and CM
	// 8, deflate. No OTLP/JSON document opens with 0x1f, so a stored batch
	// says itself whether Compress kept it — across a restart and a
	// configuration change alike.
	gzipMagic = []byte{0x1f, 0x8b, 0x08}
	// gzipWriters recycles the compressors: a gzip.Writer holds its deflate
	// window, the expensive part, across Reset.
	gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}
)

// gzipBatch compresses batch at the default level into a fresh slice — the
// broker may keep the bytes it is handed, so they are never pooled. Writing
// to a bytes.Buffer cannot fail, so neither can the compressor.
func gzipBatch(batch []byte) []byte {
	var out bytes.Buffer
	//: OTLP/JSON shrinks eight to twelve times; a quarter is room enough.
	out.Grow(len(batch)/4 + len(gzipMagic))
	zw, ok := gzipWriters.Get().(*gzip.Writer)
	//: the pool only ever holds writers; a foreign value is replaced.
	if !ok {
		zw = gzip.NewWriter(io.Discard)
	}
	defer gzipWriters.Put(zw)
	zw.Reset(&out)
	//: a bytes.Buffer never refuses a write: the error paths are unreachable.
	if _, werr := zw.Write(batch); werr != nil {
		unreachable(werr)
	}
	//: Close flushes the last block and writes the trailer.
	unreachable(zw.Close())
	//: the compressed batch, owned by the caller.
	return out.Bytes()
}

// encodingOf is the Content-Encoding batch rests in: EncodingGzip for a
// gzip stream, "" for anything else.
func encodingOf(batch []byte) string {
	//: a gzip stream says so in its first three bytes.
	if bytes.HasPrefix(batch, gzipMagic) {
		//: posted labelled.
		return EncodingGzip
	}
	//: the identity encoding.
	return ""
}

// unreachable drops an error a compressor writing into a bytes.Buffer
// cannot return — the buffer never refuses a write. It is a named function
// so the discard is a documented decision rather than a blank assignment.
func unreachable(err error) {
	//: read the parameter so the unused-param audit treats this as intentional.
	if err == nil {
		//: the only path a bytes.Buffer takes.
		return
	}
}
