// Package bson_test — fuzz coverage for Unmarshal, the attacker-facing half of
// the codec. Beyond "it does not panic", which the runtime enforces, the
// target asserts that validity is a property of the bytes and not of the
// target, and that whatever decodes re-encodes into a document that decodes
// to the same bytes again.
package bson_test

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/codec/bson"
)

// fuzzRecord is a typed target with one field of each family the decoder
// converts into.
type fuzzRecord struct {
	// I takes any number.
	I int64 `bson:"i"`
	// U takes a non-negative number.
	U uint16 `bson:"u"`
	// F takes any number.
	F float32 `bson:"f,truncate"`
	// S takes a string, a symbol, an ObjectID or a generic binary.
	S string `bson:"s"`
	// B takes a generic binary or a string.
	B []byte `bson:"b"`
	// T takes a datetime, an int64, a timestamp or a string.
	T time.Time `bson:"t"`
	// A takes anything.
	A any `bson:"a"`
	// L takes an array.
	L []int `bson:"l"`
	// M takes a document.
	M map[string]any `bson:"m"`
	// N takes a document.
	N *fuzzRecord `bson:"n"`
	// Rest takes every other element.
	Rest map[string]any `bson:",inline"`
}

// fuzzSeeds are well-formed documents of every type, and the malformed shapes
// the validator exists to refuse.
func fuzzSeeds(f *testing.F) [][]byte {
	f.Helper()
	full, err := bson.New().Marshal(bson.D{
		{Key: "i", Value: int32(1)},
		{Key: "u", Value: int64(2)},
		{Key: "f", Value: 0.1},
		{Key: "s", Value: "s"},
		{Key: "b", Value: []byte{1}},
		{Key: "t", Value: time.Unix(1, 0)},
		{Key: "a", Value: bson.A{bson.D{{Key: "x", Value: nil}}}},
		{Key: "l", Value: bson.A{int32(1)}},
		{Key: "m", Value: bson.M{"k": bson.Regex{Pattern: "p", Options: "xi"}}},
		{Key: "n", Value: bson.D{{Key: "i", Value: true}}},
		{Key: "o", Value: bson.ObjectID{1}},
		{Key: "d", Value: bson.NewDecimal128(1, 2)},
		{Key: "ts", Value: bson.Timestamp{T: 1, I: 2}},
		{Key: "bin", Value: bson.Binary{Subtype: bson.BinaryOld, Data: []byte{3}}},
		{Key: "c", Value: bson.CodeWithScope{Code: "c", Scope: bson.D{{Key: "z", Value: bson.MinKey{}}}}},
		{Key: "p", Value: bson.DBPointer{DB: "d", Pointer: bson.ObjectID{2}}},
		{Key: "js", Value: bson.JavaScript("x")},
		{Key: "sy", Value: bson.Symbol("y")},
		{Key: "u2", Value: bson.Undefined{}},
		{Key: "mx", Value: bson.MaxKey{}},
	})
	if err != nil {
		f.Fatalf("seed: %v", err)
	}
	seeds := [][]byte{full, {5, 0, 0, 0, 0}, nil}
	//: the malformed shapes, from the specification's decode-error corpus.
	for _, h := range []string{
		"0500000001", "090000000862000200", "0c0000000261000000000000",
		"0e00000002610002000000e90000", "07000000800000", "13000000057800060000000203000000ffff00",
		"1800000003666f6f000f0000001062617200ffffff7f0000", "160000000f61000d0000000100000000050000000000",
		"0c0000000b61006162006300", "1200000002666f6f00040000006261720000deadbeef",
	} {
		b, err := hex.DecodeString(h)
		if err != nil {
			f.Fatalf("seed %q: %v", h, err)
		}
		seeds = append(seeds, b)
	}
	return seeds
}

// FuzzUnmarshal drives arbitrary bytes into three targets.
//
// Invariants asserted:
//
//  1. TARGET-INDEPENDENT VALIDITY — *any and map[string]any accept exactly the
//     same inputs: whether bytes are one well-formed document does not depend
//     on what they are decoded into. A typed target may refuse more (a value
//     its field cannot hold) but never accepts what the generic ones refuse.
//  2. STABLE RE-ENCODING — what decodes into *any re-encodes, and that
//     encoding decodes and re-encodes to itself byte for byte: the codec's own
//     output is a fixed point (regex options sorted, nothing else changed).
func FuzzUnmarshal(f *testing.F) {
	for _, seed := range fuzzSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		codc := bson.New()
		var generic any
		genericErr := codc.Unmarshal(data, &generic)
		var asMap map[string]any
		mapErr := codc.Unmarshal(data, &asMap)
		var typed fuzzRecord
		typedErr := codc.Unmarshal(data, &typed)
		//: invariant 1: validity does not depend on the generic target.
		if (genericErr == nil) != (mapErr == nil) {
			t.Fatalf("*any err=%v but map err=%v for %x", genericErr, mapErr, data)
		}
		//: invariant 1: a typed target never accepts what the generic refuse.
		if genericErr != nil {
			if typedErr == nil {
				t.Fatalf("the typed target accepted %x, which *any refused: %v", data, genericErr)
			}
			return
		}
		first, err := codc.Marshal(generic)
		//: invariant 2: what decoded re-encodes.
		if err != nil {
			t.Fatalf("Marshal of the decoded value = %v (input %x)", err, data)
		}
		var again any
		if err := codc.Unmarshal(first, &again); err != nil {
			t.Fatalf("Unmarshal of the codec's own output = %v (%x)", err, first)
		}
		second, err := codc.Marshal(again)
		//: invariant 2: a fixed point.
		if err != nil || !bytes.Equal(first, second) {
			t.Fatalf("re-encoding is not stable: %x then %x (%v)", first, second, err)
		}
	})
}
