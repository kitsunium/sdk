// Package protobuf_test measures the quarantined Protobuf codec against the two
// in-tree codecs a consumer gets by default, JSON and CBOR
// (internal/service/codec/*). All three implement the same core/codec.Codec
// port, but Protobuf is SCHEMA-BOUND — it encodes proto.Message values and
// nothing else — so a shared Go value is impossible and the fixtures are two
// representations of the same document: a map[string]any for JSON and CBOR, and
// the structpb.Struct built from it for Protobuf. The conversion between them is
// measured too, because a caller pays it and no Marshal benchmark shows it.
//
// A second family measures a REAL generated message
// (descriptorpb.FileDescriptorProto), because structpb is protobuf's slowest
// shape and judging the format on it alone would be a libel.
package protobuf_test

import (
	"strconv"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/known/structpb"

	corecodec "github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/cbor"
	sdkjson "github.com/kitsunium/sdk/internal/service/codec/json"
	"github.com/kitsunium/sdk/third-party/codec/protobuf"
)

const (
	// benchMediumFields is the scalar field count of the medium document, on
	// top of which it also carries a nested object and a list.
	benchMediumFields int = 24
	// benchLargeFields is the scalar field count of the large document — a
	// wide, flat record, which is the shape a wire codec meets most often.
	benchLargeFields int = 200
	// benchNestedFields is the field count of the medium document's nested
	// object, so nesting is represented rather than merely mentioned.
	benchNestedFields int = 4
	// benchListItems is the element count of the medium document's list.
	benchListItems int = 8
	// benchGenMessages is the message count of the generated-message fixture.
	benchGenMessages int = 16
	// benchGenFields is the field count of each generated message. 16 x 12 is
	// a mid-size .proto file's descriptor.
	benchGenFields int = 12
	// benchAppendCap pre-sizes the Append destination so the row measures the
	// encoder rather than the allocator growing a buffer.
	benchAppendCap int = 64 << 10
)

// benchWire is a package-level sink for encoded bytes so the compiler cannot
// elide the encode being measured.
var benchWire []byte

// benchDecoded is a package-level sink for decoded documents.
var benchDecoded any

// benchStruct is a package-level sink for constructed structpb values.
var benchStruct *structpb.Struct

// benchDoc pairs one logical document with its two representations: the map
// JSON and CBOR encode, and the proto.Message Protobuf encodes.
type benchDoc struct {
	name    string
	asMap   map[string]any
	asProto *structpb.Struct
}

// smallMap is the three-field document — the fixture the package's own
// round-trip test uses.
func smallMap() map[string]any {
	//: float64 rather than int, because structpb.NewStruct converts every
	//: number to a double; using float64 on both sides keeps the two
	//: representations carrying the same values rather than merely similar ones.
	return map[string]any{"id": "kitsunium", "n": float64(42), "ok": true}
}

// wideMap builds a flat document of n string-keyed scalar fields.
func wideMap(n int) map[string]any {
	//: alternating types keep the encoder off a single-kind fast path.
	m := make(map[string]any, n)
	for i := range n {
		key := "field_" + strconv.Itoa(i)
		switch i % 3 {
		case 0:
			m[key] = "value-" + strconv.Itoa(i)
		case 1:
			m[key] = float64(i)
		default:
			m[key] = i%2 == 0
		}
	}
	return m
}

// mediumMap is a wide document plus one nested object and one list, so the
// medium row exercises recursion as well as width.
func mediumMap() map[string]any {
	m := wideMap(benchMediumFields)
	//: a nested object and a list are what separate a document format from a
	//: row format; a fixture without them measures only half a codec.
	m["nested"] = wideMap(benchNestedFields)
	items := make([]any, 0, benchListItems)
	for i := range benchListItems {
		items = append(items, "item-"+strconv.Itoa(i))
	}
	m["items"] = items
	return m
}

// toStruct converts a map fixture into the proto.Message Protobuf can encode.
func toStruct(tb testing.TB, m map[string]any) *structpb.Struct {
	tb.Helper()
	s, err := structpb.NewStruct(m)
	if err != nil {
		tb.Fatalf("structpb.NewStruct: %v", err)
	}
	return s
}

// benchDocs is the size ladder every row walks.
func benchDocs(tb testing.TB) []benchDoc {
	tb.Helper()
	small, medium, large := smallMap(), mediumMap(), wideMap(benchLargeFields)
	return []benchDoc{
		{"small", small, toStruct(tb, small)},
		{"medium", medium, toStruct(tb, medium)},
		{"large", large, toStruct(tb, large)},
	}
}

// generatedFixture builds a real generated message — a .proto file descriptor
// with typed, optional and repeated fields and two enums, which is what a caller
// who ran protoc actually holds.
func generatedFixture() *descriptorpb.FileDescriptorProto {
	msgs := make([]*descriptorpb.DescriptorProto, 0, benchGenMessages)
	for i := range benchGenMessages {
		fields := make([]*descriptorpb.FieldDescriptorProto, 0, benchGenFields)
		for j := range benchGenFields {
			fields = append(fields, &descriptorpb.FieldDescriptorProto{
				Name:     proto.String("field_" + strconv.Itoa(j)),
				Number:   proto.Int32(int32(j) + 1),
				Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
				Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
				JsonName: proto.String("field" + strconv.Itoa(j)),
			})
		}
		msgs = append(msgs, &descriptorpb.DescriptorProto{
			Name:  proto.String("Message" + strconv.Itoa(i)),
			Field: fields,
		})
	}
	//: a descriptor is not a toy: it nests three levels, repeats at two of
	//: them, and carries enums — the features a hand-rolled fixture omits.
	return &descriptorpb.FileDescriptorProto{
		Name:        proto.String("bench.proto"),
		Package:     proto.String("kitsunium.bench"),
		Syntax:      proto.String("proto3"),
		MessageType: msgs,
	}
}

// TestReportWireSize records the encoded size of each fixture under each codec.
// Byte count is the axis `go test -bench` cannot report, and for a wire format
// it is half the reason to adopt one, so it is measured in the ordinary suite
// exactly as third-party/transform reports its compression ratios.
func TestReportWireSize(t *testing.T) {
	t.Parallel()
	pb, jsn, cb := protobuf.New(), sdkjson.New(), cbor.New()
	for _, doc := range benchDocs(t) {
		pbOut, perr := pb.Marshal(doc.asProto)
		if perr != nil {
			t.Fatalf("protobuf.Marshal(%s): %v", doc.name, perr)
		}
		jsOut, jerr := jsn.Marshal(doc.asMap)
		if jerr != nil {
			t.Fatalf("json.Marshal(%s): %v", doc.name, jerr)
		}
		cbOut, cerr := cb.Marshal(doc.asMap)
		if cerr != nil {
			t.Fatalf("cbor.Marshal(%s): %v", doc.name, cerr)
		}
		t.Logf("wire-size %-6s protobuf=%d json=%d cbor=%d", doc.name, len(pbOut), len(jsOut), len(cbOut))
	}
	gen := generatedFixture()
	genPB, gperr := pb.Marshal(gen)
	if gperr != nil {
		t.Fatalf("protobuf.Marshal(generated): %v", gperr)
	}
	genJS, gjerr := jsn.Marshal(gen)
	if gjerr != nil {
		t.Fatalf("json.Marshal(generated): %v", gjerr)
	}
	t.Logf("wire-size %-6s protobuf=%d json=%d", "gen", len(genPB), len(genJS))
}

// BenchmarkMarshal encodes each fixture with each codec, Protobuf over the
// structpb representation and JSON/CBOR over the equivalent map.
func BenchmarkMarshal(b *testing.B) {
	pb, jsn, cb := protobuf.New(), sdkjson.New(), cbor.New()
	for _, doc := range benchDocs(b) {
		benchMarshalMessage(b, "protobuf/"+doc.name, pb, doc.asProto)
		benchMarshalMap(b, "json/"+doc.name, jsn, doc.asMap)
		benchMarshalMap(b, "cbor/"+doc.name, cb, doc.asMap)
	}
}

// benchMarshalMessage runs one encode row over a proto.Message. The parameter is
// typed rather than any, because the two representations this file compares are
// exactly the point and erasing them would hide it.
func benchMarshalMessage(b *testing.B, name string, c corecodec.Codec, v proto.Message) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := c.Marshal(v)
			if err != nil {
				b.Fatalf("Marshal: %v", err)
			}
			benchWire = out
		}
	})
}

// benchMarshalMap runs one encode row over the map representation.
func benchMarshalMap(b *testing.B, name string, c corecodec.Codec, v map[string]any) {
	b.Helper()
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := c.Marshal(v)
			if err != nil {
				b.Fatalf("Marshal: %v", err)
			}
			benchWire = out
		}
	})
}

// BenchmarkUnmarshal decodes bytes that the SAME codec produced, into a fresh
// target every iteration.
func BenchmarkUnmarshal(b *testing.B) {
	pb, jsn, cb := protobuf.New(), sdkjson.New(), cbor.New()
	for _, doc := range benchDocs(b) {
		benchUnmarshalStruct(b, "protobuf/"+doc.name, pb, doc.asProto)
		benchUnmarshalMap(b, "json/"+doc.name, jsn, doc.asMap)
		benchUnmarshalMap(b, "cbor/"+doc.name, cb, doc.asMap)
	}
}

// benchUnmarshalStruct seeds from v and decodes into a fresh structpb.Struct.
func benchUnmarshalStruct(b *testing.B, name string, c corecodec.Codec, v *structpb.Struct) {
	b.Helper()
	seed, err := c.Marshal(v)
	if err != nil {
		b.Fatalf("seed Marshal: %v", err)
	}
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			dst := &structpb.Struct{}
			if uerr := c.Unmarshal(seed, dst); uerr != nil {
				b.Fatalf("Unmarshal: %v", uerr)
			}
			benchDecoded = dst
		}
	})
}

// benchUnmarshalMap seeds from v and decodes into a fresh map.
func benchUnmarshalMap(b *testing.B, name string, c corecodec.Codec, v map[string]any) {
	b.Helper()
	seed, err := c.Marshal(v)
	if err != nil {
		b.Fatalf("seed Marshal: %v", err)
	}
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			dst := map[string]any{}
			if uerr := c.Unmarshal(seed, &dst); uerr != nil {
				b.Fatalf("Unmarshal: %v", uerr)
			}
			benchDecoded = dst
		}
	})
}

// BenchmarkNewStruct measures the map -> proto.Message conversion. It is not
// part of any codec, which is exactly why it is here: a caller holding a
// map[string]any must pay it before Marshal can be called at all, and a Marshal
// benchmark alone would hide it.
func BenchmarkNewStruct(b *testing.B) {
	for _, doc := range benchDocs(b) {
		b.Run(doc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				s, err := structpb.NewStruct(doc.asMap)
				if err != nil {
					b.Fatalf("NewStruct: %v", err)
				}
				benchStruct = s
			}
		})
	}
}

// BenchmarkAppend measures the optional codec.Appender path into a buffer that
// already has capacity.
func BenchmarkAppend(b *testing.B) {
	pb, jsn, cb := protobuf.New(), sdkjson.New(), cbor.New()
	for _, doc := range benchDocs(b) {
		benchAppendMessage(b, "protobuf/"+doc.name, pb, doc.asProto)
		benchAppendMap(b, "json/"+doc.name, jsn, doc.asMap)
		benchAppendMap(b, "cbor/"+doc.name, cb, doc.asMap)
	}
}

// benchAppendMessage runs one Appender row over a proto.Message, skipping a
// codec that does not implement the optional interface.
func benchAppendMessage(b *testing.B, name string, c corecodec.Codec, v proto.Message) {
	b.Helper()
	appender, ok := c.(corecodec.Appender)
	if !ok {
		return
	}
	b.Run(name, func(b *testing.B) {
		dst := make([]byte, 0, benchAppendCap)
		b.ReportAllocs()
		for b.Loop() {
			out, err := appender.Append(dst[:0], v)
			if err != nil {
				b.Fatalf("Append: %v", err)
			}
			benchWire = out
		}
	})
}

// benchAppendMap runs one Appender row over the map representation.
func benchAppendMap(b *testing.B, name string, c corecodec.Codec, v map[string]any) {
	b.Helper()
	appender, ok := c.(corecodec.Appender)
	if !ok {
		return
	}
	b.Run(name, func(b *testing.B) {
		dst := make([]byte, 0, benchAppendCap)
		b.ReportAllocs()
		for b.Loop() {
			out, err := appender.Append(dst[:0], v)
			if err != nil {
				b.Fatalf("Append: %v", err)
			}
			benchWire = out
		}
	})
}

// BenchmarkGeneratedMarshal encodes a REAL generated message with Protobuf and
// with JSON. This is the row that says what codegen buys, and it is the fair
// comparison structpb cannot give.
func BenchmarkGeneratedMarshal(b *testing.B) {
	gen := generatedFixture()
	benchMarshalMessage(b, "protobuf", protobuf.New(), gen)
	benchMarshalMessage(b, "json", sdkjson.New(), gen)
}

// BenchmarkGeneratedUnmarshal decodes the generated message back.
func BenchmarkGeneratedUnmarshal(b *testing.B) {
	pb := protobuf.New()
	seed, err := pb.Marshal(generatedFixture())
	if err != nil {
		b.Fatalf("seed Marshal: %v", err)
	}
	b.ReportAllocs()
	for b.Loop() {
		dst := &descriptorpb.FileDescriptorProto{}
		if uerr := pb.Unmarshal(seed, dst); uerr != nil {
			b.Fatalf("Unmarshal: %v", uerr)
		}
		benchDecoded = dst
	}
}
