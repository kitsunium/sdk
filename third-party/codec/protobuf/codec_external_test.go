package protobuf_test

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/third-party/codec/protobuf"
)

// sample builds a structpb.Struct proto.Message fixture (no codegen needed).
func sample(t *testing.T) *structpb.Struct {
	t.Helper()
	//: a JSON-like document structpb can represent losslessly.
	s, err := structpb.NewStruct(map[string]any{"id": "kitsunium", "n": float64(42), "ok": true})
	//: construction must succeed for the round-trip to be meaningful.
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

// TestRoundTrip encodes a proto.Message and decodes it back without loss.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	c := protobuf.New()
	want := sample(t)
	data, err := c.Marshal(want)
	//: encode must succeed for a proto.Message.
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := &structpb.Struct{}
	//: decode back into a fresh message.
	if uerr := c.Unmarshal(data, got); uerr != nil {
		t.Fatalf("Unmarshal: %v", uerr)
	}
	//: proto.Equal is the canonical message comparison.
	if !proto.Equal(want, got) {
		t.Errorf("round-trip mismatch got=%v want=%v", got, want)
	}
}

// TestRegisteredViaImport verifies the codec self-registers under name/MIME/ext.
func TestRegisteredViaImport(t *testing.T) {
	t.Parallel()
	//: name lookup.
	if _, ok := codec.Lookup("protobuf"); !ok {
		t.Error("Format \"protobuf\" not registered")
	}
	//: MIME lookup.
	if _, ok := codec.LookupMIME("application/protobuf"); !ok {
		t.Error("MIME application/protobuf not registered")
	}
	//: extension lookup.
	if _, ok := codec.LookupExt(".pb"); !ok {
		t.Error("extension .pb not registered")
	}
}

// TestMarshalRejectsNonMessage confirms a non-proto.Message surfaces the sentinel.
func TestMarshalRejectsNonMessage(t *testing.T) {
	t.Parallel()
	//: Protobuf is schema-bound — a plain map is not a message.
	_, err := protobuf.New().Marshal(map[string]any{"x": 1})
	//: the error must surface and name the marshal reason.
	if err == nil || !strings.Contains(err.Error(), "PROTOBUF_MARSHAL_FAILED") {
		t.Fatalf("Marshal(non-message) err=%v, want PROTOBUF_MARSHAL_FAILED", err)
	}
}

// TestUnmarshalSizeCap surfaces PROTOBUF_SIZE_EXCEEDED above the 10 MiB cap.
func TestUnmarshalSizeCap(t *testing.T) {
	t.Parallel()
	//: an 11 MiB buffer trips the cap before the decoder runs.
	big := make([]byte, (10<<20)+1)
	err := protobuf.New().Unmarshal(big, &structpb.Struct{})
	//: the cap error must surface.
	if err == nil || !strings.Contains(err.Error(), "PROTOBUF_SIZE_EXCEEDED") {
		t.Fatalf("Unmarshal(oversized) err=%v, want PROTOBUF_SIZE_EXCEEDED", err)
	}
}
