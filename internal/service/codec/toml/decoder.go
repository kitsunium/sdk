// Package toml: decoder.go adapts pelletier's *Decoder to codec.Decoder.
package toml

import (
	"errors"
	"io"

	gotoml "github.com/pelletier/go-toml/v2"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// tomlDecoder wraps *gotoml.Decoder so it satisfies codec.Decoder.
// pelletier's Decoder reads the entire input on the first Decode call —
// subsequent calls return io.EOF. We reflect that semantics via the
// sticky `done` latch.
type tomlDecoder struct {
	inner *gotoml.Decoder
	//: sticky EOF flag so More() returns false after the one Decode succeeds.
	done bool
}

// Decode reads the entire TOML document into v.
//
// Params:
//   - v: pointer destination.
//
// Returns:
//   - error: UnmarshalFailed on failure, io.EOF on stream end.
func (d *tomlDecoder) Decode(v any) (err error) {
	//: drained latch short-circuits the stdlib-style stream contract.
	if d.done {
		//: stream already consumed.
		return io.EOF
	}
	//: delegate to the library.
	derr := d.inner.Decode(v)
	//: EOF semantics preserved.
	if errors.Is(derr, io.EOF) {
		//: mark drained for subsequent More() calls.
		d.done = true
		//: return io.EOF untouched.
		return io.EOF
	}
	//: success fast-path.
	if derr == nil {
		//: single-document stream — mark drained after success.
		d.done = true
		//: nothing to wrap.
		return nil
	}
	//: mark drained so More() stops a dec.More()/Decode() loop on error.
	d.done = true
	//: wrap the library error for reason-based matching.
	return errs.Wrap(derr, errs.WrapParams{
		Code:    CodeTOMLUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "TOML decoding failed",
		Private: "service/codec/toml.Decoder.Decode: pelletier/go-toml/v2 returned an error",
	})
}

// More reports whether a Decode call would produce another document.
//
// Returns:
//   - bool: false once Decode has run to completion.
func (d *tomlDecoder) More() (ok bool) {
	//: reflect the drained latch.
	return !d.done
}
