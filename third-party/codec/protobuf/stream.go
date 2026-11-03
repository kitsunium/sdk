package protobuf

import (
	"bufio"
	"errors"
	"io"
	"math"

	"google.golang.org/protobuf/encoding/protodelim"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxVarintBytes is the longest a message's varint length prefix can be.
const maxVarintBytes int = 10

// protobufEncoder writes one varint-delimited message per Encode — the
// layout of google.golang.org/protobuf/encoding/protodelim, of a file of
// records and of a gRPC-style frame sequence (#905).
type protobufEncoder struct {
	// w receives the stream; the encoder never closes it.
	w io.Writer
}

// protobufDecoder reads one varint-delimited message per Decode.
type protobufDecoder struct {
	// r is the stream, buffered so a message can be read in place.
	r *bufio.Reader
	// done latches once the stream drained or failed.
	done bool
}

// NewEncoder wraps w in a streaming codec.Encoder: each Encode writes one
// message, prefixed by its length as a varint, in one Write; a message that
// fails to encode writes nothing.
func (*protobufCodec) NewEncoder(w io.Writer) codec.Encoder {
	//: stateless besides the writer.
	return &protobufEncoder{w: w}
}

// NewDecoder wraps r in a streaming codec.Decoder: each Decode reads one
// varint-delimited message, refusing a declared length past 10 MiB before
// reading its body.
func (*protobufCodec) NewDecoder(r io.Reader) codec.Decoder {
	br, ok := r.(*bufio.Reader)
	//: protodelim reads the length a byte at a time and the body in place.
	if !ok {
		br = bufio.NewReader(r)
	}
	//: ready.
	return &protobufDecoder{r: br}
}

// Encode writes v, a proto.Message, as one delimited message: its length as
// a varint, then the message, in one Write; a value that is not a message,
// or that fails to encode, writes nothing.
func (e *protobufEncoder) Encode(v any) error {
	msg, ok := v.(proto.Message)
	//: Protobuf is schema-bound — the value must be a generated message.
	if !ok {
		//: nothing written.
		return marshalFailed(nil, "third-party/codec/protobuf.Encoder.Encode: value does not satisfy proto.Message")
	}
	opts := proto.MarshalOptions{UseCachedSize: true}
	size := opts.Size(msg)
	//: never write a message the decoder would refuse to read back.
	if size > maxProtobufBytes {
		//: nothing written.
		return sizeExceeded("third-party/codec/protobuf.Encoder.Encode: the message exceeds maxProtobufBytes")
	}
	buf := scratch.AcquireBuffer()
	buf.Grow(maxVarintBytes + size)
	b := protowire.AppendVarint(buf.AvailableBuffer(), uint64(size)) //nolint:gosec // size is non-negative
	b, merr := opts.MarshalAppend(b, msg)
	//: the library's failure; nothing written.
	if merr != nil {
		scratch.ReleaseBuffer(buf)
		//: wrapped.
		return marshalFailed(merr, "third-party/codec/protobuf.Encoder.Encode: proto.MarshalAppend returned an error")
	}
	_, werr := e.w.Write(b)
	scratch.ReleaseBuffer(buf)
	//: the writer's failure.
	if werr != nil {
		//: wrapped.
		return marshalFailed(werr, "third-party/codec/protobuf.Encoder.Encode: the writer returned an error")
	}
	//: one message on the stream.
	return nil
}

// Close implements codec.Encoder; a delimited stream has no trailer.
func (*protobufEncoder) Close() error {
	//: nothing to flush.
	return nil
}

// Decode reads the next delimited message into v, a proto.Message,
// refusing a declared length past maxProtobufBytes before the body is read.
func (d *protobufDecoder) Decode(v any) error {
	//: a nil or non-pointer target is the caller's error, refused first (#1101).
	if terr := codec.CheckTarget(v); terr != nil {
		return terr
	}
	msg, ok := v.(proto.Message)
	//: the target must be a generated message; nothing consumed.
	if !ok {
		//: refused.
		return unmarshalFailed(nil, "third-party/codec/protobuf.Decoder.Decode: target does not satisfy proto.Message")
	}
	//: a drained or failed stream stays so.
	if d.done {
		//: the clean end.
		return io.EOF
	}
	uerr := protodelim.UnmarshalOptions{MaxSize: int64(maxProtobufBytes)}.UnmarshalFrom(d.r, msg)
	//: decoded.
	if uerr == nil {
		//: one message.
		return nil
	}
	d.done = true
	//: a clean end before any byte of a message.
	if errors.Is(uerr, io.EOF) {
		//: io.EOF verbatim, as every streaming codec returns it.
		return io.EOF
	}
	//: a declared length past the bound, refused before the body is read.
	if tooLarge, isTooLarge := errors.AsType[*protodelim.SizeTooLargeError](uerr); isTooLarge {
		//: typed, with the length the stream declared.
		return sizeExceeded("third-party/codec/protobuf.Decoder.Decode: a declared message length exceeds maxProtobufBytes",
			errs.Int64("len", int64(min(tooLarge.Size, math.MaxInt64))))
	}
	//: a malformed or truncated message.
	return unmarshalFailed(uerr, "third-party/codec/protobuf.Decoder.Decode: protodelim.UnmarshalFrom returned an error")
}

// More reports whether another message may follow.
func (d *protobufDecoder) More() bool {
	//: latched.
	if d.done {
		//: nothing more.
		return false
	}
	_, perr := d.r.Peek(1)
	//: a byte waits, or the stream ended.
	return perr == nil
}

// marshalFailed wraps cause with the marshal sentinel's code.
func marshalFailed(cause error, private string) error {
	//: typed.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeProtobufMarshalFailed,
		Reason:  "PROTOBUF_MARSHAL_FAILED",
		Public:  "Protobuf encoding failed",
		Private: private,
	})
}

// unmarshalFailed wraps cause with the unmarshal sentinel's code.
func unmarshalFailed(cause error, private string) error {
	//: typed.
	return errs.Wrap(cause, errs.WrapParams{
		Code:    CodeProtobufUnmarshalFailed,
		Reason:  "PROTOBUF_UNMARSHAL_FAILED",
		Public:  "Protobuf decoding failed",
		Private: private,
	})
}

// sizeExceeded builds the size sentinel's error, carrying the bound and any
// field that locates the refusal.
func sizeExceeded(private string, fields ...errs.FieldValue) error {
	//: typed.
	return errs.Wrap(nil, errs.WrapParams{
		Code:    CodeProtobufSizeExceeded,
		Reason:  "SIZE_EXCEEDED",
		Public:  "Protobuf input exceeds size limit",
		Private: private,
	}, append(fields, errs.Int("cap", maxProtobufBytes))...)
}
