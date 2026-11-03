package bson_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/bson"
)

// strictTarget is a struct a strict decode must match exactly.
type strictTarget struct {
	A int32 `bson:"a"`
	B int32 `bson:"b"`
}

// int32Element is one int32 element.
func int32Element(name string, v int32) []byte {
	b := append([]byte{0x10}, name...)
	return binary.LittleEndian.AppendUint32(append(b, 0), uint32(v))
}

// document frames elements as a BSON document.
func document(elements ...[]byte) []byte {
	body := bytes.Join(elements, nil)
	out := binary.LittleEndian.AppendUint32(nil, uint32(4+len(body)+1))
	return append(append(out, body...), 0)
}

// embedded is an element holding doc as an embedded document (or an array).
func embedded(typ byte, name string, doc []byte) []byte {
	return append(append(append([]byte{typ}, name...), 0), doc...)
}

// TestNewStrict is #1106: the registered codec reads a repeated element
// name last-wins and skips an element no field claims. The strict instance
// refuses both, at any depth, and still decodes an exact match; the
// registered codec is unchanged.
func TestNewStrict(t *testing.T) {
	t.Parallel()
	strict := bson.NewStrict()
	if registered, _ := codec.Lookup("bson"); registered == strict {
		t.Fatal("the strict instance must not be the registered codec")
	}
	twice := document(int32Element("a", 1), int32Element("a", 2))
	dup := []struct {
		name   string
		data   []byte
		target func() any
	}{
		{"a name twice, into a map", twice, func() any { return new(map[string]any) }},
		{"a name twice, into a struct", twice, func() any { return new(strictTarget) }},
		{"a name twice, in an embedded document", document(embedded(0x03, "x", twice)), func() any { return new(map[string]any) }},
		{"a name twice, in a document in an array", document(embedded(0x04, "x", document(embedded(0x03, "0", twice)))), func() any { return new(map[string]any) }},
	}
	for _, tc := range dup {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := strict.Unmarshal(tc.data, tc.target()); !errs.HasReason(err, "DUPLICATE_KEY") {
				t.Fatalf("strict: want DUPLICATE_KEY, got %v", err)
			}
			if err := bson.New().Unmarshal(tc.data, tc.target()); err != nil {
				t.Fatalf("registered: want last-wins, got %v", err)
			}
		})
	}
	unknown := document(int32Element("a", 1), int32Element("b", 2), int32Element("c", 3))
	var got strictTarget
	if err := strict.Unmarshal(unknown, &got); !errs.HasReason(err, "UNKNOWN_MEMBER") {
		t.Fatalf("strict, an unknown member: want UNKNOWN_MEMBER, got %v", err)
	}
	if err := bson.New().Unmarshal(unknown, &got); err != nil {
		t.Fatalf("registered, an unknown member: %v", err)
	}
	exact := document(int32Element("a", 1), int32Element("b", 2))
	if err := strict.Unmarshal(exact, &got); err != nil || got != (strictTarget{A: 1, B: 2}) {
		t.Fatalf("strict, an exact match: %+v (%v)", got, err)
	}
	dec := strict.(codec.StreamingCodec).NewDecoder(bytes.NewReader(append(append([]byte{}, exact...), twice...)))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("strict stream, first document: %v", err)
	}
	if err := dec.Decode(&got); !errs.HasReason(err, "DUPLICATE_KEY") {
		t.Fatalf("strict stream, second document: want DUPLICATE_KEY, got %v", err)
	}
}
