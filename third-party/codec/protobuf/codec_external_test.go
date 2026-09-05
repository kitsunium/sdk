// Package protobuf_test — the codec as a consumer sees it: one registered
// singleton reachable by name, MIME type and file extension.
package protobuf_test

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/third-party/codec/protobuf"
)

// sample builds a structpb.Struct proto.Message fixture (no codegen needed).
func sample(t *testing.T, fields map[string]any) *structpb.Struct {
	t.Helper()
	s, err := structpb.NewStruct(fields)
	//: construction must succeed for the round trip to be meaningful.
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

// TestNew pins that the package registers itself on import and that every
// documented key resolves to the SAME singleton. Three keys resolving to three
// codecs would still pass a per-key lookup test while doubling the work a
// content-negotiating caller does.
func TestNew(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		lookup func() (codec.Codec, bool)
	}
	tests := []tc{
		{"by format name", func() (codec.Codec, bool) { return codec.Lookup("protobuf") }},
		{"by MIME type", func() (codec.Codec, bool) { return codec.LookupMIME("application/protobuf") }},
		{"by the x- MIME alias", func() (codec.Codec, bool) { return codec.LookupMIME("application/x-protobuf") }},
		{"by file extension", func() (codec.Codec, bool) { return codec.LookupExt(".pb") }},
	}
	want := protobuf.New()
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got, ok := c.lookup()
		if !ok {
			t.Fatalf("lookup %s missed — the codec did not self-register", c.name)
		}
		//: the same singleton, not merely an equivalent codec.
		if got != want {
			t.Errorf("lookup %s resolved a different instance", c.name)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: New is stateless, so repeated calls must not mint new instances.
	if protobuf.New() != want {
		t.Error("New() returned a different instance on a second call")
	}
}

// TestRoundTrip is the consumer-level contract: what this codec writes, it can
// read back. Marshal and Unmarshal are tested individually inside the package;
// what only shows up here is that the two agree.
func TestRoundTrip(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		fields map[string]any
	}
	tests := []tc{
		{"a mixed document", map[string]any{"id": "kitsunium", "n": float64(42), "ok": true}},
		{"an empty document", map[string]any{}},
		{"a nested document", map[string]any{"outer": map[string]any{"inner": "v"}}},
		{"a list-valued field", map[string]any{"xs": []any{"a", "b"}}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		codc := protobuf.New()
		want := sample(t, c.fields)

		data, err := codc.Marshal(want)
		if err != nil {
			t.Fatalf("Marshal = %v, want nil", err)
		}
		got := &structpb.Struct{}
		if uerr := codc.Unmarshal(data, got); uerr != nil {
			t.Fatalf("Unmarshal = %v, want nil", uerr)
		}
		//: proto messages carry unexported state, so equality goes through the
		//: library rather than through ==.
		if !proto.Equal(want, got) {
			t.Errorf("the round trip turned %v into %v", want, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
