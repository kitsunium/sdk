package rpc

import (
	"encoding/binary"
	"io"
	"math"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corerpc "github.com/kitsunium/sdk/internal/core/proc/ipc/rpc"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// protocolVersion is the version a hello says; a peer saying another is
// refused (FORMAT_REFUSED).
const protocolVersion byte = 1

// The kinds of frame (ADR 0218), numbered from 1 in this order — the wire's,
// never to be reordered: the hello, a call and a stream's request, the
// answers — a reply, an item, a stream's end, an error —, a caller's cancel,
// and a ping with its pong.
const (
	kindHello byte = iota + 1
	kindCall
	kindStream
	kindReply
	kindItem
	kindEnd
	kindError
	kindCancel
	kindPing
	kindPong
)

// The widths of the wire's fixed parts, in bytes.
const (
	// lenBytes is the length prefix, which counts what follows it.
	lenBytes int = 4
	// headerBytes is kind:u8 | id:u64 | mlen:u8, what follows the length
	// before the method.
	headerBytes int = 10
	// idOffset and mlenOffset are where the id and the method's length sit in
	// the header.
	idOffset   int = 1
	mlenOffset int = 9
	// budgetBytes is the budget a call's body opens with: nanoseconds, u64.
	budgetBytes int = 8
	// helloBodyBytes is a hello's body: version:u8 | maxFrame:u32.
	helloBodyBytes int = 5
	// errorFixedBytes is an error's body before its reason: code:u32 | rlen:u8.
	errorFixedBytes int = 5
	// maxNameBytes is the longest method, format or reason a u8 length says.
	maxNameBytes int = math.MaxUint8
	// minFrameBytes is the smallest MaxFrameBytes a configuration may set: a
	// header, a budget and room for a body.
	minFrameBytes int = 64
	// maxFrameLimit is the largest a u32 length can say.
	maxFrameLimit int64 = math.MaxUint32
	// frameStartBytes is the capacity a frame being built starts with: a
	// small call fits, a larger one grows it.
	frameStartBytes int = 256
)

// frameReader is what a frame is read from: its first byte alone, so that
// FrameTimeout starts with it, then the rest whole.
type frameReader interface {
	io.Reader
	io.ByteReader
}

// wireFormat is the format a connection agreed on, as a codec.Appender
// whatever its codec: a frame is built in place through the codec's own
// Append when it has one, through Marshal otherwise. Its errors are
// CODEC_FAILED.
type wireFormat struct {
	codec.Codec
}

// frameValue is one frame read off the wire. method and body come from one
// allocation per frame, made only once the length was found within bounds.
type frameValue struct {
	// kind is the frame's kind, one of the kind constants.
	kind byte
	// id is the call the frame belongs to; 0 for the connection itself.
	id uint64
	// method is a call's method, a hello's format; empty otherwise.
	method string
	// body is what follows the method.
	body []byte
}

// readFrame reads the next frame: it blocks for its first byte as long as the
// connection is idle, then calls started — the caller arms FrameTimeout there
// — and refuses a length past maxFrame before reading or allocating a byte of
// the rest. A peer that hung up between frames is io.EOF, as the reader says.
func readFrame(r frameReader, maxFrame int, started func()) (frameValue, error) {
	first, err := r.ReadByte()
	//: an idle connection that ended: not a violation.
	if err != nil {
		return frameValue{}, err
	}
	started()
	var prefix [lenBytes]byte
	prefix[0] = first
	//: the rest of the length.
	if _, err := io.ReadFull(r, prefix[1:]); err != nil {
		return frameValue{}, unexpectedEOF(err)
	}
	n := binary.BigEndian.Uint32(prefix[:])
	//: the bound holds on the length alone: nothing is allocated for it.
	if uint64(n) > uint64(maxFrame) {
		return frameValue{}, errs.Wrap(corerpc.FrameTooLarge, errs.WrapParams{},
			errs.Int64("length", int64(n)), errs.Int("limit", maxFrame))
	}
	//: too short to hold a header.
	if int(n) < headerBytes {
		return frameValue{}, violation("a frame shorter than its header")
	}
	buf := make([]byte, n)
	//: within bounds: the frame, whole.
	if _, err := io.ReadFull(r, buf); err != nil {
		return frameValue{}, unexpectedEOF(err)
	}
	return parseFrame(buf)
}

// parseFrame splits a frame read whole into its parts.
func parseFrame(buf []byte) (frameValue, error) {
	end := headerBytes + int(buf[mlenOffset])
	//: a method longer than the frame.
	if end > len(buf) {
		return frameValue{}, violation("a method longer than its frame")
	}
	return frameValue{
		kind:   buf[0],
		id:     binary.BigEndian.Uint64(buf[idOffset:mlenOffset]),
		method: string(buf[headerBytes:end]),
		body:   buf[end:],
	}, nil
}

// unexpectedEOF reads an end inside a frame as what it is: a frame cut short.
func unexpectedEOF(err error) error {
	//: an EOF after the frame's first byte is not a clean hang-up.
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

// beginFrame appends a frame's length placeholder and header to dst; the
// body follows, then finishFrame writes the length.
func beginFrame(dst []byte, kind byte, id uint64, method string) []byte {
	dst = append(dst, 0, 0, 0, 0, kind)
	dst = binary.BigEndian.AppendUint64(dst, id)
	dst = append(dst, byte(len(method)))
	return append(dst, method...)
}

// finishFrame writes the frame's length and refuses one past what the peer
// reads (peerMax): nothing of it is sent.
func finishFrame(frame []byte, peerMax int) ([]byte, error) {
	n := len(frame) - lenBytes
	//: the peer said in its hello how long a frame it reads.
	if n > peerMax {
		return nil, errs.Wrap(corerpc.FrameTooLarge, errs.WrapParams{},
			errs.Int("length", n), errs.Int("limit", peerMax), errs.String("side", "sending"))
	}
	binary.BigEndian.PutUint32(frame, uint32(n))
	return frame, nil
}

// emptyFrame is a whole frame with no body: a cancel, an end, a ping, a pong.
func emptyFrame(kind byte, id uint64) []byte {
	frame := beginFrame(make([]byte, 0, lenBytes+headerBytes), kind, id, "")
	binary.BigEndian.PutUint32(frame, uint32(headerBytes))
	return frame
}

// Append appends v, encoded, to dst: through the codec's Appender when it
// has one, so the frame is the only buffer.
func (f wireFormat) Append(dst []byte, v any) (appended []byte, err error) {
	//: the codec writes into the frame.
	if a, ok := f.Codec.(codec.Appender); ok {
		appended, err = a.Append(dst, v)
	} else {
		var b []byte
		b, err = f.Marshal(v)
		appended = append(dst, b...)
	}
	//: the format refused the value.
	if err != nil {
		return nil, codecFailed("encode", err)
	}
	return appended, nil
}

// helloFrame is a hello: the format as its method, the version and the
// largest frame this end reads as its body.
func helloFrame(format codec.Format, maxFrame int) []byte {
	frame := beginFrame(make([]byte, 0, lenBytes+headerBytes+len(format)+helloBodyBytes), kindHello, 0, string(format))
	frame = append(frame, protocolVersion)
	frame = binary.BigEndian.AppendUint32(frame, uint32(maxFrame))
	binary.BigEndian.PutUint32(frame, uint32(len(frame)-lenBytes))
	return frame
}

// parseHello reads a hello's body: the peer's version, and the largest frame
// it reads.
func parseHello(f frameValue) (peerMax int, err error) {
	//: a hello is first, alone, and whole.
	if f.kind != kindHello || f.id != 0 || len(f.body) != helloBodyBytes {
		return 0, violation("a connection that does not open with a hello")
	}
	//: a version this end does not speak.
	if f.body[0] != protocolVersion {
		return 0, errs.Wrap(corerpc.FormatRefused, errs.WrapParams{}, errs.Int("version", int(f.body[0])))
	}
	peerMax = int(binary.BigEndian.Uint32(f.body[1:]))
	//: a peer that reads no frame a call fits in.
	if peerMax < minFrameBytes {
		return 0, violation("a hello naming a frame too small for any call")
	}
	return peerMax, nil
}

// errorFrame is an error answer for id: the code, reason and public text of
// err when it carries an SDK code, REMOTE_FAILED's otherwise. Its Private
// half and its fields never leave this process.
func errorFrame(id uint64, err error) []byte {
	code, ok := errs.CodeOf(err)
	reason, _ := errs.ReasonOf(err)
	public := errs.PublicOf(err)
	//: an error without a code, or with a reason the wire cannot carry.
	if !ok || reason == "" || len(reason) > maxNameBytes {
		code, reason, public = corerpc.CodeRemoteFailed, corerpc.RemoteFailed.Reason(), corerpc.RemoteFailed.Public()
	}
	frame := beginFrame(make([]byte, 0, lenBytes+headerBytes+errorFixedBytes+len(reason)+len(public)), kindError, id, "")
	frame = binary.BigEndian.AppendUint32(frame, uint32(code))
	frame = append(frame, byte(len(reason)))
	frame = append(frame, reason...)
	frame = append(frame, public...)
	binary.BigEndian.PutUint32(frame, uint32(len(frame)-lenBytes))
	return frame
}

// remoteError rebuilds the error an error frame carries: the same code,
// reason and public text, so errs.HasCode answers on this side as on the
// server's, and REMOTE_FAILED when what the frame says is not a valid SDK
// error.
func remoteError(f frameValue) error {
	//: shorter than its fixed part.
	if len(f.body) < errorFixedBytes {
		return violation("an error frame shorter than its code")
	}
	code := errs.Code(binary.BigEndian.Uint32(f.body))
	end := errorFixedBytes + int(f.body[errorFixedBytes-1])
	//: a reason longer than the frame.
	if end > len(f.body) {
		return violation("an error frame's reason longer than the frame")
	}
	reason, public := string(f.body[errorFixedBytes:end]), string(f.body[end:])
	rebuilt := errs.NewRuntime(code, reason, public, "service/proc/ipc/rpc: the peer answered this error",
		errs.Bool("remote", true))
	//: what the peer sent does not make a valid error: say only that it failed.
	if got, _ := errs.CodeOf(rebuilt); got != code {
		return errs.Wrap(corerpc.RemoteFailed, errs.WrapParams{}, errs.String("remote_code", code.String()))
	}
	return rebuilt
}

// violation is PROTOCOL_VIOLATION with its rule.
func violation(rule string) error {
	return errs.Wrap(corerpc.ProtocolViolation, errs.WrapParams{}, errs.String("rule", rule))
}

// codecFailed is CODEC_FAILED for the direction that failed. The format's
// own error — itself an SDK error, whose code would win a wrap — travels as a
// field, for the log.
func codecFailed(direction string, cause error) error {
	return errs.Wrap(corerpc.CodecFailed, errs.WrapParams{},
		errs.String("direction", direction), errs.String("cause", cause.Error()))
}

// misconfigured is MISCONFIGURED with its rule.
func misconfigured(rule string) error {
	return errs.Wrap(corerpc.Misconfigured, errs.WrapParams{}, errs.String("rule", rule))
}
