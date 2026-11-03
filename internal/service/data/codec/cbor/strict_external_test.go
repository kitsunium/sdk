package cbor_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// strictTarget is a struct a strict decode must match exactly.
type strictTarget struct {
	A int `cbor:"a"`
	B int `cbor:"b"`
}

// strictHex decodes a spaced hex fixture.
func strictHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatalf("bad fixture %q: %v", s, err)
	}
	return b
}

// TestNewStrict is #1106: RFC 8949 §5.6 leaves duplicate keys to the
// protocol, and the registered codec reads them last-wins. The strict
// instance refuses a repeat — written with two heads or two chunkings
// included — and a member no field claims, and still decodes an exact match;
// the registered codec is unchanged.
func TestNewStrict(t *testing.T) {
	t.Parallel()
	strict := cbor.NewStrict()
	if registered, _ := codec.Lookup("cbor"); registered == strict {
		t.Fatal("the strict instance must not be the registered codec")
	}
	dup := []struct {
		name   string
		data   string
		target func() any
	}{
		{"a text key twice, into any", "a2 61 61 01 61 61 02", func() any { return new(any) }},
		{"a text key twice, into a map", "a2 61 61 01 61 61 02", func() any { return new(map[string]int) }},
		{"a text key twice, into a struct", "a2 61 61 01 61 61 02", func() any { return new(strictTarget) }},
		{"one text in one chunk and in two", "a2 61 61 01 7f 60 61 61 ff 02", func() any { return new(map[string]int) }},
		{"one integer with two heads", "a2 01 01 18 01 02", func() any { return new(map[int]int) }},
		{"a repeat in an indefinite map", "bf 61 61 01 61 61 02 ff", func() any { return new(map[string]int) }},
		{"a repeat inside an array", "81 a2 61 61 01 61 61 02", func() any { return new([]map[string]int) }},
	}
	for _, tc := range dup {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data := strictHex(t, tc.data)
			if err := strict.Unmarshal(data, tc.target()); !errs.HasReason(err, "DUPLICATE_KEY") {
				t.Fatalf("strict: want DUPLICATE_KEY, got %v", err)
			}
			if err := cbor.New().Unmarshal(data, tc.target()); err != nil {
				t.Fatalf("registered: want last-wins, got %v", err)
			}
		})
	}
	unknown := strictHex(t, "a3 61 61 01 61 62 02 61 63 03")
	var got strictTarget
	if err := strict.Unmarshal(unknown, &got); !errs.HasReason(err, "UNKNOWN_MEMBER") {
		t.Fatalf("strict, an unknown member: want UNKNOWN_MEMBER, got %v", err)
	}
	if err := cbor.New().Unmarshal(unknown, &got); err != nil {
		t.Fatalf("registered, an unknown member: %v", err)
	}
	exact := strictHex(t, "a2 61 61 01 61 62 02")
	if err := strict.Unmarshal(exact, &got); err != nil || got != (strictTarget{A: 1, B: 2}) {
		t.Fatalf("strict, an exact match: %+v (%v)", got, err)
	}
	dec := strict.(codec.StreamingCodec).NewDecoder(bytes.NewReader(append(exact, strictHex(t, "a2 61 61 01 61 61 02")...)))
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("strict stream, first item: %v", err)
	}
	if err := dec.Decode(&got); !errs.HasReason(err, "DUPLICATE_KEY") {
		t.Fatalf("strict stream, second item: want DUPLICATE_KEY, got %v", err)
	}
}
