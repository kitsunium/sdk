package protobuf

import (
	"google.golang.org/protobuf/proto"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxProtobufBytes caps Unmarshal input for untrusted payloads (CWE-400). The
// decoder reads declared field lengths before validating them, so size-limiting
// the buffer is the primary memory-exhaustion defence. 10 MiB matches the other
// library-backed codecs.
const maxProtobufBytes int = 10 << 20

// Codec is the registered Protobuf singleton.
var Codec codec.Codec = codec.Register(&protobufCodec{})

// protobufCodec is the concrete Codec implementation for Protobuf. Stateless.
type protobufCodec struct{}

// New returns the Protobuf codec singleton.
func New() codec.Codec {
	//: stateless — one singleton serves the whole process.
	return Codec
}

// Name returns the canonical Format identifier.
func (*protobufCodec) Name() string {
	//: the registered Format string.
	return "protobuf"
}

// MIMETypes returns a defensive copy of the MIME alias list.
func (*protobufCodec) MIMETypes() []string {
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{"application/protobuf", "application/x-protobuf"}
}

// Extensions returns a defensive copy of the file-extension list.
func (*protobufCodec) Extensions() []string {
	//: a fresh literal the caller owns; no package variable is read when Register calls this.
	return []string{".pb"}
}

// Marshal encodes v as Protobuf wire bytes. v must satisfy proto.Message;
// anything else returns PROTOBUF_MARSHAL_FAILED. It is Append onto nil: proto
// sizes the message, then fills one buffer of that size.
func (c *protobufCodec) Marshal(v any) (encoded []byte, err error) {
	out, aerr := c.Append(nil, v)
	//: an empty message is an empty, non-nil slice, as proto.Marshal returns it.
	if aerr == nil && out == nil {
		//: zero bytes on the wire.
		return []byte{}, nil
	}
	//: the one write path.
	return out, aerr
}

// Unmarshal decodes Protobuf wire bytes into v after the size cap. v must
// satisfy proto.Message.
func (*protobufCodec) Unmarshal(data []byte, v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	//: CWE-400 defence — refuse oversized inputs before the decoder allocates.
	if len(data) > maxProtobufBytes {
		//: surface the size sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeProtobufSizeExceeded,
			Reason:  "SIZE_EXCEEDED",
			Public:  "Protobuf input exceeds size limit",
			Private: "third-party/codec/protobuf.Unmarshal: len(data) > maxProtobufBytes",
		})
	}
	//: the target must be a generated message.
	msg, ok := v.(proto.Message)
	//: a non-message target cannot be decoded into.
	if !ok {
		//: surface the unmarshal sentinel for a non-message target.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeProtobufUnmarshalFailed,
			Reason:  "PROTOBUF_UNMARSHAL_FAILED",
			Public:  "Protobuf decoding failed",
			Private: "third-party/codec/protobuf.Unmarshal: target does not satisfy proto.Message",
		})
	}
	//: delegate to the library decoder.
	uerr := proto.Unmarshal(data, msg)
	//: success fast-path.
	if uerr == nil {
		//: nothing to wrap.
		return nil
	}
	//: wrap the library failure with the dotted-quad code.
	return errs.Wrap(uerr, errs.WrapParams{
		Code:    CodeProtobufUnmarshalFailed,
		Reason:  "PROTOBUF_UNMARSHAL_FAILED",
		Public:  "Protobuf decoding failed",
		Private: "third-party/codec/protobuf.Unmarshal: google.golang.org/protobuf/proto.Unmarshal returned an error",
	})
}

// Append encodes v as Protobuf and appends it onto dst (optional Appender),
// through proto.MarshalOptions.MarshalAppend: a dst with room for the message
// is written in place, with no allocation. A failure leaves dst untouched.
func (*protobufCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: Protobuf is schema-bound — the value must be a generated message.
	msg, ok := v.(proto.Message)
	//: a non-message value cannot be encoded.
	if !ok {
		//: dst untouched.
		return dst, marshalFailed(nil, "third-party/codec/protobuf.Append: value does not satisfy proto.Message")
	}
	//: proto's default options: the bytes proto.Marshal writes.
	out, merr := proto.MarshalOptions{}.MarshalAppend(dst, msg)
	//: the library's failure.
	if merr != nil {
		//: dst's prior contents, never a torn tail.
		return dst, marshalFailed(merr, "third-party/codec/protobuf.Append: proto.MarshalAppend returned an error")
	}
	//: the message after dst's contents.
	return out, nil
}
