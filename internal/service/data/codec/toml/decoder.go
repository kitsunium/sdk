// Package toml — the streaming codec.Decoder: the reader is one TOML
// document, read whole on the first Decode, within maxDocumentBytes.
package toml

import (
	"errors"
	"io"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tomlDecoder reads its reader as one document. TOML has no separator between
// documents, so the first Decode consumes the stream and every later one
// returns io.EOF; the sticky done latch makes More report false from then on.
type tomlDecoder struct {
	// r is the document.
	r io.Reader
	// done is set by the first Decode, whatever it returned.
	done bool
}

// Decode reads the whole document into v.
func (d *tomlDecoder) Decode(v any) error {
	//: the stream was consumed by an earlier call.
	if d.done {
		//: stream already consumed.
		return io.EOF
	}
	d.done = true
	// One byte past the cap is enough to tell a document at the cap from one
	// over it, without reading the rest of an endless stream.
	data, err := io.ReadAll(io.LimitReader(d.r, int64(maxDocumentBytes)+1))
	//: a reader that reports an end of input, even wrapped, ends the stream.
	if errors.Is(err, io.EOF) {
		//: return io.EOF untouched.
		return io.EOF
	}
	//: any other read failure.
	if err != nil {
		//: UNMARSHAL_FAILED over the reader's error.
		return errs.Wrap(err, errs.WrapParams{
			Code:    CodeTOMLUnmarshalFailed,
			Reason:  UnmarshalFailed.Reason(),
			Public:  UnmarshalFailed.Public(),
			Private: privateReadFailed,
		})
	}
	//: parse and decode; a document past the cap is refused there.
	return unmarshal(data, v)
}

// More reports whether a Decode call would produce another document.
func (d *tomlDecoder) More() bool {
	//: reflect the drained latch.
	return !d.done
}
