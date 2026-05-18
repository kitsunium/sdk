// Package msgpack: decoder.go adapts vmihailenco's *Decoder to codec.Decoder.
package msgpack

import (
	"errors"
	"io"

	gomsgpack "github.com/vmihailenco/msgpack/v5"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// msgpackDecoder wraps *gomsgpack.Decoder so it satisfies codec.Decoder.
type msgpackDecoder struct {
	inner *gomsgpack.Decoder
	//: sticky EOF flag so More() returns false once the stream drains.
	done bool
}

// Decode reads the next MessagePack object into v.
//
// Params:
//   - v: pointer destination.
//
// Returns:
//   - error: UnmarshalFailed on failure, io.EOF when the stream is drained.
func (d *msgpackDecoder) Decode(v any) error {
	//: delegate to the library.
	derr := d.inner.Decode(v)
	//: EOF latch toggles the done flag.
	if errors.Is(derr, io.EOF) {
		//: remember we reached the stream end.
		d.done = true
		//: return io.EOF untouched.
		return io.EOF
	}
	//: success fast-path.
	if derr == nil {
		//: nothing to wrap.
		return nil
	}
	//: mark drained so More() stops a dec.More()/Decode() loop on error.
	d.done = true
	//: wrap the library error for reason-based matching.
	return errs.Wrap(derr, errs.WrapParams{
		Code:    CodeMsgPackUnmarshalFailed,
		Reason:  "UNMARSHAL_FAILED",
		Public:  "MessagePack decoding failed",
		Private: "service/codec/msgpack.Decoder.Decode: vmihailenco/msgpack/v5 returned an error",
	})
}

// More reports whether additional items are still available.
//
// Returns:
//   - bool: false once Decode has returned io.EOF.
func (d *msgpackDecoder) More() bool {
	//: reflect the sticky EOF latch.
	return !d.done
}
