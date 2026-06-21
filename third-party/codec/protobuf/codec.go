// Package protobuf wraps google.golang.org/protobuf as a codec.Codec
// implementation. It lives under third-party/codec/ (root module) and is
// OPT-IN: a consumer blank-imports it to register the "protobuf" Format —
// pkg/v1/codec does NOT pull it. The reason is the schema-bound contract, not
// dep weight: Protobuf only encodes proto.Message values, so it cannot honour
// the universal "every registered codec round-trips any Go struct" guarantee
// the default registry enforces. A non-message value surfaces
// PROTOBUF_MARSHAL_FAILED / PROTOBUF_UNMARSHAL_FAILED. Non-streaming (the wire
// format is length-unframed; framing is a caller concern); implements Appender.
// ADR 0023.
package protobuf

import (
	"slices"

	"google.golang.org/protobuf/proto"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxProtobufBytes caps Unmarshal input for untrusted payloads (CWE-400). The
// decoder reads declared field lengths before validating them, so size-limiting
// the buffer is the primary memory-exhaustion defence. 10 MiB matches the other
// library-backed codecs.
const maxProtobufBytes int = 10 << 20

var (
	// Codec is the registered Protobuf singleton.
	Codec codec.Codec = codec.Register(&protobufCodec{})

	// mimeTypes is hoisted so MIMETypes does not allocate the literal per call.
	mimeTypes = []string{"application/protobuf", "application/x-protobuf"}

	// extensions is hoisted for the same reason.
	extensions = []string{".pb"}
)

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
	//: callers must not mutate the package-level table.
	return slices.Clone(mimeTypes)
}

// Extensions returns a defensive copy of the file-extension list.
func (*protobufCodec) Extensions() []string {
	//: callers must not mutate the package-level table.
	return slices.Clone(extensions)
}

// Marshal encodes v as Protobuf wire bytes. v must satisfy proto.Message;
// anything else returns PROTOBUF_MARSHAL_FAILED.
func (*protobufCodec) Marshal(v any) (encoded []byte, err error) {
	//: Protobuf is schema-bound — the value must be a generated message.
	msg, ok := v.(proto.Message)
	//: a non-message value cannot be encoded.
	if !ok {
		//: surface the marshal sentinel for a non-message value.
		return nil, errs.Wrap(nil, errs.WrapParams{
			Code:    CodeProtobufMarshalFailed,
			Reason:  "PROTOBUF_MARSHAL_FAILED",
			Public:  "Protobuf encoding failed",
			Private: "third-party/codec/protobuf.Marshal: value does not satisfy proto.Message",
		})
	}
	//: delegate to the library encoder.
	out, merr := proto.Marshal(msg)
	//: success fast-path.
	if merr == nil {
		//: hand back the wire bytes.
		return out, nil
	}
	//: wrap the library failure with the dotted-quad code.
	return nil, errs.Wrap(merr, errs.WrapParams{
		Code:    CodeProtobufMarshalFailed,
		Reason:  "PROTOBUF_MARSHAL_FAILED",
		Public:  "Protobuf encoding failed",
		Private: "third-party/codec/protobuf.Marshal: google.golang.org/protobuf/proto.Marshal returned an error",
	})
}

// Unmarshal decodes Protobuf wire bytes into v after the size cap. v must
// satisfy proto.Message.
func (*protobufCodec) Unmarshal(data []byte, v any) error {
	//: CWE-400 defence — refuse oversized inputs before the decoder allocates.
	if len(data) > maxProtobufBytes {
		//: surface the size sentinel.
		return errs.Wrap(nil, errs.WrapParams{
			Code:    CodeProtobufSizeExceeded,
			Reason:  "PROTOBUF_SIZE_EXCEEDED",
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

// Append encodes v as Protobuf and appends it onto dst (optional Appender).
// proto exposes no public append API, so the encode allocates; a failure leaves
// dst untouched.
func (c *protobufCodec) Append(dst []byte, v any) (appended []byte, err error) {
	//: snapshot dst so a failure restores the caller's buffer exactly.
	origLen := len(dst)
	//: encode (Marshal already wraps any error with the Protobuf sentinel).
	out, merr := c.Marshal(v)
	//: propagate a marshal failure, leaving dst untouched.
	if merr != nil {
		//: restore dst to its original length.
		return dst[:origLen], merr
	}
	//: append the encoded message onto dst.
	return append(dst, out...), nil
}
