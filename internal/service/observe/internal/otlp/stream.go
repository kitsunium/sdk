package otlp

import (
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

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
