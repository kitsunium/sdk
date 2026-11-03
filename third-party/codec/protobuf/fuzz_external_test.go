package protobuf_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/third-party/codec/protobuf"
)

// FuzzUnmarshalStructpb is #1159: Unmarshal hands untrusted bytes to
// proto.Unmarshal. Any input into a structpb.Struct yields nil or a typed
// refusal — never a panic — and a message that decodes re-marshals and
// decodes back equal.
func FuzzUnmarshalStructpb(f *testing.F) {
	for _, m := range []map[string]any{
		{},
		{"a": 1.0, "b": "two", "c": true, "d": nil},
		{"nested": map[string]any{"list": []any{1.0, "x", map[string]any{"k": false}}}},
	} {
		s, err := structpb.NewStruct(m)
		if err != nil {
			f.Fatalf("NewStruct: %v", err)
		}
		seed, err := proto.Marshal(s)
		if err != nil {
			f.Fatalf("Marshal: %v", err)
		}
		f.Add(seed)
	}
	//: a truncated length, an unknown wire type, a group start, deep nesting.
	f.Add([]byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f})
	f.Add([]byte{0x0f})
	f.Add([]byte{0x0b, 0x0c})
	f.Add([]byte{0x0a, 0x05, 0x0a, 0x03, 0x0a, 0x01, 0x0a})
	f.Fuzz(func(t *testing.T, data []byte) {
		var got structpb.Struct
		err := protobuf.New().Unmarshal(data, &got)
		//: every refusal is one of the module's decode codes.
		if err != nil {
			if !errs.HasCode(err, protobuf.CodeProtobufUnmarshalFailed) && !errs.HasCode(err, protobuf.CodeProtobufSizeExceeded) {
				t.Fatalf("untyped refusal: %v", err)
			}
			return
		}
		out, merr := protobuf.New().Marshal(&got)
		if merr != nil {
			t.Fatalf("re-Marshal of a decoded message: %v", merr)
		}
		var back structpb.Struct
		if uerr := protobuf.New().Unmarshal(out, &back); uerr != nil || !proto.Equal(&back, &got) {
			t.Fatalf("round trip differs (%v)", uerr)
		}
	})
}
