package protobuf_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/third-party/codec/protobuf"
)

// streamMessages are three messages of different shapes.
func streamMessages(t *testing.T) []*structpb.Struct {
	t.Helper()
	var out []*structpb.Struct
	for _, m := range []map[string]any{{"a": 1.0}, {"b": "two", "c": true}, {"d": []any{1.0, "x"}}} {
		s, err := structpb.NewStruct(m)
		if err != nil {
			t.Fatalf("NewStruct: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// TestStreamRoundTrip is #905: the module registered the Format with no
// stream form, so a file of records or a frame sequence — varint-delimited
// messages, protodelim's layout — could not go through codec.NewEncoder and
// NewDecoder. Three messages encode to the concatenation of their delimited
// forms and decode back, More reporting false after the third.
func TestStreamRoundTrip(t *testing.T) {
	t.Parallel()
	sc, ok := protobuf.New().(codec.StreamingCodec)
	if !ok {
		t.Fatal("protobuf is not a StreamingCodec")
	}
	msgs := streamMessages(t)
	var buf bytes.Buffer
	enc := sc.NewEncoder(&buf)
	var want []byte
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatalf("Encode: %v", err)
		}
		body, err := protobuf.New().Marshal(m)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want = append(protowire.AppendVarint(want, uint64(len(body))), body...)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	//: map entries are written in no fixed order, so the framing is checked
	//: by length, and the messages by decoding them.
	if buf.Len() != len(want) {
		t.Fatalf("the stream holds %d bytes, the delimited messages %d", buf.Len(), len(want))
	}
	dec := sc.NewDecoder(bytes.NewReader(buf.Bytes()))
	for i, m := range msgs {
		if !dec.More() {
			t.Fatalf("More false before message %d", i)
		}
		var got structpb.Struct
		if err := dec.Decode(&got); err != nil || !proto.Equal(&got, m) {
			t.Fatalf("message %d: %v (%v)", i, &got, err)
		}
	}
	if dec.More() {
		t.Fatal("More true after the third message")
	}
	var extra structpb.Struct
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		t.Fatalf("Decode past the end: want io.EOF, got %v", err)
	}
}

// TestStreamRefusesAnOversizedMessage declares a message past the 10 MiB
// bound: the decoder refuses it typed before reading its body.
func TestStreamRefusesAnOversizedMessage(t *testing.T) {
	t.Parallel()
	sc := protobuf.New().(codec.StreamingCodec)
	frame := protowire.AppendVarint(nil, 10<<20+1)
	var got structpb.Struct
	err := sc.NewDecoder(bytes.NewReader(frame)).Decode(&got)
	if !errs.HasCode(err, protobuf.CodeProtobufSizeExceeded) {
		t.Fatalf("want SIZE_EXCEEDED, got %v", err)
	}
	if err := sc.NewEncoder(io.Discard).Encode("not a message"); !errs.HasCode(err, protobuf.CodeProtobufMarshalFailed) {
		t.Fatalf("a non-message: want PROTOBUF_MARSHAL_FAILED, got %v", err)
	}
	truncated := append(protowire.AppendVarint(nil, 5), 0x0a)
	if err := sc.NewDecoder(bytes.NewReader(truncated)).Decode(&got); !errs.HasCode(err, protobuf.CodeProtobufUnmarshalFailed) {
		t.Fatalf("a truncated frame: want PROTOBUF_UNMARSHAL_FAILED, got %v", err)
	}
}

// TestAppendWritesWhereMarshalWould holds Append to what Marshal writes,
// after the caller's prefix, and leaves dst untouched on a refusal. The
// message is a Struct of two fields, whose map entries protobuf-go writes in
// no fixed order — the codec does not ask for Deterministic — so two encodes
// of it may differ byte for byte (one run in eight did, comparing bytes): the
// body is held to Marshal's length and read back, as TestStreamRoundTrip
// holds its frames.
func TestAppendWritesWhereMarshalWould(t *testing.T) {
	t.Parallel()
	ap := protobuf.New().(codec.Appender)
	m := streamMessages(t)[1]
	want, err := protobuf.New().Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	out, err := ap.Append([]byte("pre"), m)
	if err != nil || len(out) < 3 || string(out[:3]) != "pre" || len(out)-3 != len(want) {
		t.Fatalf("Append: %q (%v), want the prefix then %d bytes", out, err, len(want))
	}
	var back structpb.Struct
	if uerr := protobuf.New().Unmarshal(out[3:], &back); uerr != nil || !proto.Equal(&back, m) {
		t.Fatalf("Append wrote a body that reads back as %v (%v)", &back, uerr)
	}
	if out, err := ap.Append([]byte("pre"), 42); err == nil || string(out) != "pre" {
		t.Fatalf("Append of a non-message: %q (%v)", out, err)
	}
}
