// Package otlp — the newline-delimited stream a writer-bound OTLP/JSON
// exporter emits.
package otlp

import (
	"io"
	"sync"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Stream writes whole OTLP/JSON documents to one io.Writer, each terminated by
// DocumentTerminator, so a stream of exports is newline-delimited JSON — what a
// file or a terminal wants, where an HTTP body wants the document alone.
//
// mu serialises the single write: an exporter port requires concurrency safety
// and the writer is caller-supplied, so two concurrent exports must not
// interleave partial documents into a non-atomic destination. Encoding happens
// before Emit is called, outside the lock, so a slow writer serialises callers
// without also serialising the work.
type Stream struct {
	// mu serialises the one dst.Write of each document.
	mu sync.Mutex
	// dst is the caller's destination.
	dst io.Writer
	// failure is the signal's wrap for a writer fault: its EXPORT_FAILED code
	// and its wording.
	failure errs.WrapParams
}

// NewStream binds a Stream to dst. failure is the wrap a writer fault leaves
// under — the calling signal's EXPORT_FAILED, in its own words.
func NewStream(dst io.Writer, failure *errs.WrapParams) *Stream {
	//: own a copy of the wrap, so the caller's value cannot change it later.
	return &Stream{dst: dst, failure: *failure}
}

// Emit writes doc followed by DocumentTerminator in ONE write, so a refusal
// upstream leaves the destination untouched and the only error surface left is
// the write itself. doc must be a document Marshal returned; it is extended in
// place by one byte, which its spare capacity usually absorbs.
func (s *Stream) Emit(doc []byte) error {
	//: terminate the document so consecutive exports do not run together.
	doc = append(doc, DocumentTerminator)
	//: single write — serialised so concurrent exports cannot interleave.
	s.mu.Lock()
	_, writeErr := s.dst.Write(doc)
	s.mu.Unlock()
	//: success fast-path.
	if writeErr == nil {
		//: document written.
		return nil
	}
	//: the writer's fault, under the signal's own code.
	return errs.Wrap(writeErr, s.failure)
}
