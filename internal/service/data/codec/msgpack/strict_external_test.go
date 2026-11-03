package msgpack_test

import (
	"bytes"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/msgpack"
)

// strictTarget is a struct a strict decode must match exactly.
type strictTarget struct {
	A int `msgpack:"a"`
	B int `msgpack:"b"`
}

// TestNewStrict is #1106: the registered codec reads a repeated key
// last-wins and skips a member no field claims — a document with two "alg"
// reads one way here and another in a peer that keeps the first. The strict
// instance refuses both, a repeat written in two forms included, and still
// decodes a document that matches exactly; the registered codec is unchanged.
func TestNewStrict(t *testing.T) {
	t.Parallel()
	strict := msgpack.NewStrict()
	if registered, _ := codec.Lookup("msgpack"); registered == strict {
		t.Fatal("the strict instance must not be the registered codec")
	}
	dup := []struct {
		name   string
		data   string
		target func() any
	}{
		{"a str key twice, into any", "82 a1 61 01 a1 61 02", func() any { return new(any) }},
		{"a str key twice, into a map", "82 a1 61 01 a1 61 02", func() any { return new(map[string]int) }},
		{"a str key twice, into a struct", "82 a1 61 01 a1 61 02", func() any { return new(strictTarget) }},
		{"a str and a bin of the same bytes", "82 a1 61 01 c4 01 61 02", func() any { return new(map[string]int) }},
		{"one integer in two widths", "82 01 01 cc 01 02", func() any { return new(map[int]int) }},
		{"a repeat inside an array", "91 82 a1 61 01 a1 61 02", func() any { return new([]map[string]int) }},
	}
	for _, tc := range dup {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := fromHex(t, tc.data)
			if err := strict.Unmarshal(data, tc.target()); !errs.HasReason(err, "DUPLICATE_KEY") {
				t.Fatalf("strict: want DUPLICATE_KEY, got %v", err)
			}
			if err := msgpack.New().Unmarshal(data, tc.target()); err != nil {
				t.Fatalf("registered: want last-wins, got %v", err)
			}
		})
	}
	unknown := fromHex(t, "83 a1 61 01 a1 62 02 a1 63 03")
	var got strictTarget
	if err := strict.Unmarshal(unknown, &got); !errs.HasReason(err, "UNKNOWN_MEMBER") {
		t.Fatalf("strict, an unknown member: want UNKNOWN_MEMBER, got %v", err)
	}
	if err := msgpack.New().Unmarshal(unknown, &got); err != nil {
		t.Fatalf("registered, an unknown member: %v", err)
	}
	exact := fromHex(t, "82 a1 61 01 a1 62 02")
	if err := strict.Unmarshal(exact, &got); err != nil || got != (strictTarget{A: 1, B: 2}) {
		t.Fatalf("strict, an exact match: %+v (%v)", got, err)
	}
	dec := strict.(codec.StreamingCodec).NewDecoder(bytes.NewReader(append(exact, unknown...)))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("strict stream, first value: %v", err)
	}
	if err := dec.Decode(&got); !errs.HasReason(err, "UNKNOWN_MEMBER") {
		t.Fatalf("strict stream, second value: want UNKNOWN_MEMBER, got %v", err)
	}
}
