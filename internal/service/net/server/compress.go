package server

import (
	"bufio"
	"compress/gzip"
	stdnet "net"
	"net/http"
	"slices"
	"strings"
	"sync"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

const (
	// contentSniffBytes is how much http.DetectContentType reads.
	contentSniffBytes int = 512
	// defaultCompressMinBytes is the smallest body compressed unless CompressValue
	// says otherwise: below about a kilobyte gzip's container costs what it saves.
	defaultCompressMinBytes int = 1 << 10
	// gzipMinLevel is gzip's smallest level; -1 is its default.
	gzipMinLevel int = gzip.BestSpeed
	// gzipMaxLevel is gzip's largest level.
	gzipMaxLevel int = gzip.BestCompression
)

// compressDefaults are the types compressed unless CompressValue names others:
// neither text/html (BREACH) nor text/event-stream, which a compressor would
// buffer.
var compressDefaults = []string{
	"text/plain", "text/css", "text/javascript", "text/csv", "text/xml",
	"application/json", "application/javascript", "application/xml",
	"image/svg+xml", "application/wasm",
}

// Compress returns a middleware for a Group.HandleHTTP handler that answers in
// gzip what a client's Accept-Encoding admits (#853): the response is buffered
// until v.MinBytes is reached or the handler flushes or ends, so a small body
// stays identity, and then chosen on its status and Content-Type, an allowlist.
// It never compresses a response without a body (HEAD, 1xx, 204, 304), one with
// a Content-Encoding or Content-Range, a text/event-stream, a type outside the
// list or one marked Cache-Control: no-transform, which is a handler's own way
// to refuse. Every response of an allowlisted type carries Vary:
// Accept-Encoding; a strong ETag on a compressed answer is weakened. Flush
// reaches the client through the gzip stream, so a flushed response stays live.
// A level or minimum out of range is CompressInvalid.
func Compress(v corenet.CompressValue) (func(http.Handler) http.Handler, error) {
	//: a level gzip does not have.
	if v.Level != 0 && v.Level != gzip.DefaultCompression && (v.Level < gzipMinLevel || v.Level > gzipMaxLevel) {
		//: refused by name.
		return nil, errs.Wrap(corenet.CompressInvalid, errs.WrapParams{}, errs.String("option", "level"))
	}
	//: a negative minimum has no meaning.
	if v.MinBytes < 0 {
		//: refused by name.
		return nil, errs.Wrap(corenet.CompressInvalid, errs.WrapParams{}, errs.String("option", "min_bytes"))
	}
	settings := newCompressSettings(v)
	//: the middleware, ready to be applied to any handler.
	return func(next http.Handler) http.Handler {
		//: one per wrapped handler.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			settings.serve(next, w, r)
		})
	}, nil
}

// compressSettings are one Compress middleware's resolved configuration, and
// the gzip writers it recycles.
type compressSettings struct {
	// level is gzip's level, resolved.
	level int
	// minBytes is the smallest body compressed, resolved.
	minBytes int
	// types is the allowlist, lower-cased.
	types []string
	// writers recycles the gzip writers: each owns several kilobytes of
	// dictionary and window.
	writers sync.Pool
}

// newCompressSettings resolves v's defaults.
func newCompressSettings(v corenet.CompressValue) *compressSettings {
	c := &compressSettings{level: v.Level, minBytes: v.MinBytes}
	//: zero is the default.
	if c.level == 0 {
		c.level = gzip.DefaultCompression
	}
	//: zero is the default.
	if c.minBytes == 0 {
		c.minBytes = defaultCompressMinBytes
	}
	source := v.Types
	//: empty selects the defaults.
	if len(source) == 0 {
		source = compressDefaults
	}
	c.types = make([]string, 0, len(source))
	//: compared case-insensitively, parameters stripped.
	for _, t := range source {
		c.types = append(c.types, strings.ToLower(strings.TrimSpace(t)))
	}
	//: ready.
	return c
}

// serve is the middleware's request.
func (c *compressSettings) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	//: a client that admits no gzip reads the handler's own bytes.
	if r.Method == http.MethodHead || !corenet.AcceptsEncoding(r.Header.Get("Accept-Encoding"), "gzip") {
		next.ServeHTTP(w, r)
		return
	}
	writer := &compressWriter{settings: c, inner: w}
	completed := false
	defer func() {
		//: a panic is net/http's to report; the partial stream is not closed.
		writer.finish(completed)
	}()
	next.ServeHTTP(writer, r)
	completed = true
}

// compressWriter delays the response until it knows enough to choose: the
// first bytes, up to minBytes, are held so a small body can stay identity, and
// the status with them.
type compressWriter struct {
	// settings is the middleware's.
	settings *compressSettings
	// inner is the ResponseWriter the answer leaves through.
	inner http.ResponseWriter
	// status is what the handler said, zero until it says; held held the bytes
	// of the undecided prefix, lazily allocated.
	status int
	held   []byte
	// decided records that the choice was made; gz, when not nil, is the gzip
	// stream it chose.
	decided bool
	gz      *gzip.Writer
	// hijacked is set when the handler took the socket over.
	hijacked bool
}

// Header implements http.ResponseWriter.
func (w *compressWriter) Header() http.Header {
	//: the handler sets the headers the choice reads.
	return w.inner.Header()
}

// WriteHeader implements http.ResponseWriter. It is held until the choice,
// unless the handler said an informational status, which goes at once.
func (w *compressWriter) WriteHeader(status int) {
	//: informational: more to come, and nothing to decide on.
	if status >= http.StatusContinue && status < http.StatusOK {
		w.inner.WriteHeader(status)
		return
	}
	//: only the first answer.
	if w.status == 0 {
		w.status = status
	}
}

// Write implements http.ResponseWriter: held while undecided, then through the
// choice.
func (w *compressWriter) Write(p []byte) (n int, err error) {
	//: decided already: straight through.
	if w.decided {
		//: gzip, or the handler's own bytes.
		return w.writeDecided(p)
	}
	//: a first write sets 200, as net/http would.
	if w.status == 0 {
		w.status = http.StatusOK
	}
	total := len(w.held) + len(p)
	//: still short and still compressible: held.
	if total < w.settings.minBytes && w.compressible() {
		w.held = append(w.held, p...)
		//: reported whole, as a buffered write is.
		return len(p), nil
	}
	//: enough bytes, or this answer could never be compressed: chosen now.
	if err := w.decide(total, p, false); err != nil {
		//: the answer failed.
		return 0, err
	}
	//: as decided.
	return w.writeDecided(p)
}

// writeDecided writes p through the decided path.
func (w *compressWriter) writeDecided(p []byte) (int, error) {
	//: gzip.
	if w.gz != nil {
		//: through the compressor.
		return w.gz.Write(p)
	}
	//: the handler's own bytes.
	return w.inner.Write(p)
}

// Flush implements http.Flusher: a flushed response stays live. The undecided
// prefix is decided now, compressed if its type admits it whatever its length:
// the handler said it is a stream, and a chunk of one is worth compressing.
func (w *compressWriter) Flush() {
	//: a flush decides.
	if !w.decided {
		swallowErr(w.decide(len(w.held), nil, true))
	}
	//: gzip's own sync point, then the socket's.
	if w.gz != nil {
		swallowErr(w.gz.Flush())
	}
	flusher, ok := w.inner.(http.Flusher)
	//: a writer that cannot flush.
	if !ok {
		//: nothing more to do.
		return
	}
	flusher.Flush()
}

// Hijack implements http.Hijacker: the socket leaves the response, undecided.
func (w *compressWriter) Hijack() (stdnet.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.inner.(http.Hijacker)
	//: a writer that cannot.
	if !ok {
		//: net/http's own refusal.
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	//: refused.
	if err != nil {
		//: the writer stays in charge.
		return nil, nil, err
	}
	w.hijacked = true
	//: the handler's now.
	return conn, rw, nil
}

// Unwrap gives net/http's ResponseController, and any handler that asks, the
// writer beneath.
func (w *compressWriter) Unwrap() http.ResponseWriter {
	//: the next writer down.
	return w.inner
}

// compressible reports whether what the handler has said so far permits
// compression: the status, and the headers it set.
func (w *compressWriter) compressible() bool {
	h := w.inner.Header()
	//: only a 200-class body, and never a partial one.
	if w.status < http.StatusOK || w.status >= http.StatusMultipleChoices || w.status == http.StatusNoContent || w.status == http.StatusPartialContent ||
		h.Get("Content-Encoding") != "" || h.Get("Content-Range") != "" || strings.Contains(strings.ToLower(h.Get("Cache-Control")), "no-transform") {
		//: refused.
		return false
	}
	contentType := h.Get("Content-Type")
	//: unset: nothing to judge yet; the sniff happens when the body is known.
	if contentType == "" {
		//: judged in decide, on the bytes.
		return true
	}
	//: the allowlist, by the type alone.
	return w.settings.allows(contentType)
}

// allows reports whether contentType, parameters ignored, is on the list.
func (c *compressSettings) allows(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	//: the event stream is never buffered: it would stop being one.
	return slices.Contains(c.types, strings.ToLower(strings.TrimSpace(media)))
}

// decide makes the choice with what is known now — total is the bytes the
// handler has said so far, first the part not yet held — writes the held status
// and bytes through it, and leaves w.decided set. flushing says the handler
// declared a stream, the minimum then not applying.
func (w *compressWriter) decide(total int, first []byte, flushing bool) error {
	w.decided = true
	h := w.inner.Header()
	//: a handler that named no type gets net/http's sniff, on the original
	//: bytes, before they are compressed into something it would call gzip.
	if h.Get("Content-Type") == "" && total > 0 {
		h.Set("Content-Type", http.DetectContentType(w.sniffPrefix(first)))
	}
	w.addVary()
	//: the choice itself: an allowlisted answer with a body worth compressing.
	if w.compressible() && total > 0 && (flushing || total >= w.settings.minBytes) {
		w.startGzip()
	}
	//: what was held goes through whichever writer was chosen.
	return w.flushHeld()
}

// sniffPrefix returns what http.DetectContentType reads: at most its 512 bytes,
// the held bytes then the first of what is being written.
func (w *compressWriter) sniffPrefix(first []byte) []byte {
	//: enough held.
	if len(w.held) >= contentSniffBytes {
		//: the held prefix.
		return w.held
	}
	//: a copy, so held stays as the handler wrote it.
	return append(slices.Clone(w.held), first[:min(contentSniffBytes-len(w.held), len(first))]...)
}

// addVary says a cache may hold this answer in two codings, for any answer of a
// type that can be compressed, small ones included.
func (w *compressWriter) addVary() {
	h := w.inner.Header()
	contentType := h.Get("Content-Type")
	//: a type outside the list, or none yet: no twin.
	if contentType == "" || !w.settings.allows(contentType) || strings.Contains(h.Get("Vary"), "Accept-Encoding") {
		//: nothing to add.
		return
	}
	h.Add("Vary", "Accept-Encoding")
}

// startGzip sets the compressed answer's headers and takes a writer.
func (w *compressWriter) startGzip() {
	h := w.inner.Header()
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length")
	h.Del("Accept-Ranges")
	//: a strong validator names the identity bytes: this is another
	//: representation (RFC 9110 §8.8.1).
	if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
		h.Set("ETag", "W/"+etag)
	}
	writer, ok := w.settings.writers.Get().(*gzip.Writer)
	//: the pool's first.
	if !ok {
		var err error
		writer, err = gzip.NewWriterLevel(w.inner, w.settings.level)
		//: a level gzip refuses, checked at construction, so never here.
		if err != nil {
			//: identity.
			return
		}
	}
	writer.Reset(w.inner)
	w.gz = writer
}

// flushHeld writes the status and the held bytes through the chosen writer.
func (w *compressWriter) flushHeld() error {
	//: a status said: now the headers are final.
	if w.status != 0 {
		w.inner.WriteHeader(w.status)
	}
	//: the first bytes.
	if len(w.held) > 0 {
		if _, err := w.writeDecided(w.held); err != nil {
			//: the answer failed.
			return err
		}
	}
	w.held = nil
	//: answered.
	return nil
}

// finish ends the response: an undecided short body goes out identity, and a
// gzip stream is closed unless the handler panicked, net/http aborting it.
func (w *compressWriter) finish(completed bool) {
	//: the handler owns the socket: nothing to write.
	if w.hijacked {
		return
	}
	//: never decided: a body shorter than the minimum, or none, as identity.
	if !w.decided && completed {
		w.decidedIdentity()
	}
	//: no gzip stream to close.
	if w.gz == nil {
		return
	}
	//: the trailer, only for an answer that finished.
	if completed {
		swallowErr(w.gz.Close())
	}
	w.settings.writers.Put(w.gz)
	w.gz = nil
}

// decidedIdentity sends a response that never reached the minimum: the status
// and held bytes through the plain path, with Vary where it applies.
func (w *compressWriter) decidedIdentity() {
	w.decided = true
	//: the sniff net/http would have done on the same bytes.
	if w.inner.Header().Get("Content-Type") == "" && len(w.held) > 0 {
		w.inner.Header().Set("Content-Type", http.DetectContentType(w.held))
	}
	w.addVary()
	swallowErr(w.flushHeld())
}
