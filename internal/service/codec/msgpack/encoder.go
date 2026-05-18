// Package msgpack: encoder.go adapts vmihailenco's *Encoder to codec.Encoder.
package msgpack

import (
	gomsgpack "github.com/vmihailenco/msgpack/v5"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// msgpackEncoder wraps *gomsgpack.Encoder so it satisfies codec.Encoder.
type msgpackEncoder struct {
	inner *gomsgpack.Encoder
}

// Encode serialises v through the wrapped encoder.
//
// Params:
//   - v: value to encode.
//
// Returns:
//   - error: MarshalFailed wrapping the library cause on failure.
func (e *msgpackEncoder) Encode(v any) error {
	//: delegate and wrap on error.
	merr := e.inner.Encode(v)
	//: success fast-path.
	if merr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library error.
	return errs.Wrap(merr, errs.WrapParams{
		Code:    CodeMsgPackMarshalFailed,
		Reason:  "MARSHAL_FAILED",
		Public:  "MessagePack encoding failed",
		Private: "service/codec/msgpack.Encoder.Encode: vmihailenco/msgpack/v5 returned an error",
	})
}

// Close is a no-op because the vmihailenco encoder does not own the writer.
//
// Returns:
//   - error: always nil.
func (*msgpackEncoder) Close() error {
	//: vmihailenco's encoder owns no writer-level state.
	return nil
}
