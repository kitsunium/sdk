// Package strictjson — the reader that bounds a document and remembers why
// it stopped.
package strictjson

import (
	"errors"
	"fmt"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// boundedReader hands the decoder at most remaining bytes of source and
// remembers what it saw: how many bytes it delivered, and the first error
// that was not the end of the input.
//
// It is given the bound plus ONE, so a document of exactly the bound decodes
// and one byte more is seen — without ever buffering past it.
type boundedReader struct {
	source    io.Reader
	failure   error
	remaining int64
	delivered int64
}

// Read reads from source, never past the remaining budget. An exhausted
// budget reads as the end of the input: the decoder stops, and verdict says
// why.
func (b *boundedReader) Read(buffer []byte) (int, error) {
	//: the budget is spent: the decoder sees an end, verdict sees the size.
	if b.remaining <= 0 {
		//: an end of input the caller never wrote.
		return 0, io.EOF
	}
	//: never ask the source for more than the budget holds.
	if int64(len(buffer)) > b.remaining {
		buffer = buffer[:b.remaining]
	}
	count, err := b.source.Read(buffer)
	b.remaining -= int64(count)
	b.delivered += int64(count)
	//: the first real failure is the one worth reporting.
	if err != nil && !errors.Is(err, io.EOF) && b.failure == nil {
		b.failure = err
	}
	//: whatever the source said.
	return count, err
}

// verdict reports what the reading alone concludes, before the decoder's
// conclusion is considered: a document longer than maxBytes, a reader that
// failed, or a document with no byte at all. nil means the decoder's verdict
// stands.
func (b *boundedReader) verdict(maxBytes int64, oversize func(error) bool) error {
	//: more arrived than the bound allows — or the source itself said the
	//: body was too large, which it does on the byte past the bound.
	if b.delivered > maxBytes || (b.failure != nil && oversize != nil && oversize(b.failure)) {
		//: the bound, never the bytes.
		return errs.Wrap(DocumentTooLarge, errs.WrapParams{}, errs.Int64(fieldLimit, maxBytes))
	}
	//: the source failed before the document ended.
	if b.failure != nil {
		//: the sentinel is the origin, so the refusal keeps its code and its
		//: 400. The source's message is NOT carried: an arbitrary reader's
		//: error may quote what it read, and fields are public — the failure's
		//: type says which kind of reader failed, and nothing it held.
		return errs.Wrap(DocumentUnreadable, errs.WrapParams{}, errs.String(fieldCause, causeOf(b.failure)))
	}
	//: not one byte.
	if b.delivered == 0 {
		//: an empty document, which a caller may read as "no body".
		return DocumentEmpty
	}
	//: the reading was fine; the decoder decides.
	return nil
}

// causeOf names a read failure by its type — *net.OpError, *errors.errorString
// — never by its message, which a reader may have built from the bytes it
// read.
func causeOf(failure error) string {
	//: the dynamic type, which holds no input.
	return fmt.Sprintf("%T", failure)
}
