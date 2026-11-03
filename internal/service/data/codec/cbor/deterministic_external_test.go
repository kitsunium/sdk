package cbor_test

import (
	"bytes"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/core/data/codec"
	corecbor "github.com/kitsunium/sdk/internal/core/data/codec/cbor"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// appendixAVector is one RFC 8949 Appendix A example: its bytes and the Go
// value that encodes to them.
type appendixAVector struct {
	name  string
	hex   string
	value any
}

// deterministicVectors are the float rows and the tag rows 0, 1, 23, 24 and
// 32 of RFC 8949 Appendix A, the shortest encoding of each.
func deterministicVectors() []appendixAVector {
	return []appendixAVector{
		{"0.0", "f90000", 0.0},
		{"-0.0", "f98000", math.Copysign(0, -1)},
		{"1.0", "f93c00", 1.0},
		{"1.1", "fb3ff199999999999a", 1.1},
		{"1.5", "f93e00", 1.5},
		{"65504.0", "f97bff", 65504.0},
		{"100000.0", "fa47c35000", 100000.0},
		{"3.4028234663852886e+38", "fa7f7fffff", 3.4028234663852886e+38},
		{"1.0e+300", "fb7e37e43c8800759c", 1.0e+300},
		{"5.960464477539063e-8", "f90001", 5.960464477539063e-8},
		{"0.00006103515625", "f90400", 0.00006103515625},
		{"-4.0", "f9c400", -4.0},
		{"-4.1", "fbc010666666666666", -4.1},
		{"Infinity", "f97c00", math.Inf(1)},
		{"NaN", "f97e00", math.NaN()},
		{"-Infinity", "f9fc00", math.Inf(-1)},
		{"tag 0", "c074323031332d30332d32315432303a30343a30305a", cbor.TagValue{Number: 0, Content: "2013-03-21T20:04:00Z"}},
		{"tag 1 integer", "c11a514b67b0", cbor.TagValue{Number: 1, Content: uint64(1363896240)}},
		{"tag 1 float", "c1fb41d452d9ec200000", cbor.TagValue{Number: 1, Content: 1363896240.5}},
		{"tag 23", "d74401020304", cbor.TagValue{Number: 23, Content: []byte{1, 2, 3, 4}}},
		{"tag 24", "d818456449455446", cbor.TagValue{Number: 24, Content: []byte("dIETF")}},
		{"tag 32", "d82076687474703a2f2f7777772e6578616d706c652e636f6d", cbor.TagValue{Number: 32, Content: "http://www.example.com"}},
	}
}

// TestNewDeterministic_appendixA is #896's acceptance: each vector encodes
// to its bytes under NewDeterministic, and its bytes decode into an
// interface and encode back to themselves.
func TestNewDeterministic_appendixA(t *testing.T) {
	t.Parallel()
	det := cbor.NewDeterministic()
	for _, tc := range deterministicVectors() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			want, _ := hex.DecodeString(tc.hex)
			got, err := det.Marshal(tc.value)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("Marshal(%v) = %x, %v; want %s", tc.value, got, err, tc.hex)
			}
			var decoded any
			if err := det.Unmarshal(want, &decoded); err != nil {
				t.Fatalf("Unmarshal(%s): %v", tc.hex, err)
			}
			again, err := det.Marshal(decoded)
			if err != nil || !bytes.Equal(again, want) {
				t.Fatalf("Marshal(Unmarshal(%s)) = %x, %v", tc.hex, again, err)
			}
		})
	}
}

// TestNewDeterministic_registeredUnchanged pins what the registered codec
// writes: a double for every float64, a single for every float32, whatever
// shorter form holds them — #896 changes nothing of its bytes.
func TestNewDeterministic_registeredUnchanged(t *testing.T) {
	t.Parallel()
	if registered, _ := codec.Lookup("cbor"); registered == cbor.NewDeterministic() {
		t.Fatal("the deterministic instance must not be the registered codec")
	}
	for _, tc := range []struct {
		value any
		want  string
	}{
		{1.0, "fb3ff0000000000000"},
		{float32(1.5), "fa3fc00000"},
		{map[string]any{"b": 1.0, "a": float32(2)}, "a26161fa400000006162fb3ff0000000000000"},
	} {
		got, err := cbor.New().Marshal(tc.value)
		if err != nil || hex.EncodeToString(got) != tc.want {
			t.Fatalf("registered Marshal(%v) = %x, %v; want %s", tc.value, got, err, tc.want)
		}
	}
	got, err := cbor.NewDeterministic().Marshal(map[string]any{"b": 1.0, "a": float32(2)})
	if err != nil || hex.EncodeToString(got) != "a26161f940006162f93c00" {
		t.Fatalf("deterministic Marshal = %x, %v", got, err)
	}
}

// TestNewDeterministic_refuses is #896: what core deterministic encoding
// forbids is refused with NOT_DETERMINISTIC, and the registered codec still
// reads every one of these documents.
func TestNewDeterministic_refuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, hex string }{
		{"23 in a one-byte head", "1817"},
		{"a length in a two-byte head", "59000161"},
		{"a tag number in a longer head", "d81701"},
		{"1.0 as a single", "fa3f800000"},
		{"1.0 as a double", "fb3ff0000000000000"},
		{"100000.0 as a double", "fb40f86a0000000000"},
		{"a NaN with a payload", "f97e01"},
		{"a double NaN", "fb7ff8000000000000"},
		{"infinity as a single", "fa7f800000"},
		{"indefinite bytes", "5f42010243030405ff"},
		{"an indefinite array", "9f01ff"},
		{"an indefinite map", "bf616101ff"},
		{"keys out of order", "a2616201616102"},
		{"a shorter key after a longer", "a262616101616202"},
		{"a repeated key", "a2616101616102"},
		{"a departure inside a tag", "c1fa3f800000"},
		{"a departure inside an array", "82f93c00fa3f800000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			data, err := hex.DecodeString(tc.hex)
			if err != nil {
				t.Fatalf("bad fixture: %v", err)
			}
			var v any
			err = cbor.NewDeterministic().Unmarshal(data, &v)
			if !errs.HasCode(err, corecbor.CodeCBORNotDeterministic) {
				t.Fatalf("NewDeterministic().Unmarshal(%s) = %v, want NOT_DETERMINISTIC", tc.hex, err)
			}
			if err := cbor.New().Unmarshal(data, &v); err != nil {
				t.Fatalf("registered Unmarshal(%s) = %v, want it read", tc.hex, err)
			}
		})
	}
}

// TestNewDeterministic_structs holds a struct to core deterministic
// encoding: its fields, declared out of key order, are written sorted by
// their encoded keys — "id" before "name" before "active" by length first —
// so the instance reads back what it writes; the registered codec keeps the
// declaration order.
func TestNewDeterministic_structs(t *testing.T) {
	t.Parallel()
	type record struct {
		Name   string  `cbor:"name"`
		Active bool    `cbor:"active"`
		ID     int     `cbor:"id"`
		Score  float64 `cbor:"score"`
		Code   int     `cbor:"7,keyasint"`
	}
	in := record{Name: "n", Active: true, ID: 1, Score: 1.5, Code: 2}
	data, err := cbor.NewDeterministic().Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	want := "a5" + "0702" + "626964" + "01" + "646e616d65" + "616e" + "6573636f7265" + "f93e00" + "66616374697665" + "f5"
	if got := hex.EncodeToString(data); got != want {
		t.Fatalf("Marshal = %s, want %s", got, want)
	}
	var out record
	if err := cbor.NewDeterministic().Unmarshal(data, &out); err != nil || out != in {
		t.Fatalf("round trip = %+v, %v", out, err)
	}
	registered, err := cbor.New().Marshal(in)
	if err != nil || hex.EncodeToString(registered)[:12] != "a5646e616d65" {
		t.Fatalf("registered Marshal = %x, %v; want the declaration order, name first", registered, err)
	}
}

// TestNewDeterministic_stream holds the stream to Unmarshal's reading: the
// encoder writes the shortest floats, the decoder refuses a departure.
func TestNewDeterministic_stream(t *testing.T) {
	t.Parallel()
	sc, ok := cbor.NewDeterministic().(codec.StreamingCodec)
	if !ok {
		t.Fatal("the deterministic instance must stream")
	}
	var out bytes.Buffer
	enc := sc.NewEncoder(&out)
	if err := enc.Encode([]any{1.0, float32(100000)}); err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(out.Bytes()); got != "82f93c00fa47c35000" {
		t.Fatalf("stream Encode = %s", got)
	}
	in, _ := hex.DecodeString("f93c00fa3f800000")
	dec := sc.NewDecoder(bytes.NewReader(in))
	var f float64
	if err := dec.Decode(&f); err != nil || f != 1 {
		t.Fatalf("first Decode = %v, %v", f, err)
	}
	if err := dec.Decode(&f); !errs.HasCode(err, corecbor.CodeCBORNotDeterministic) {
		t.Fatalf("second Decode = %v, want NOT_DETERMINISTIC", err)
	}
}

// TestTag is #896: an application writes and reads its own tags — a COSE
// structure, here tag 98 (COSE_Sign) — while the registered codec still
// reads an unknown tag into an interface as its content.
func TestTag(t *testing.T) {
	t.Parallel()
	cose := cbor.TagValue{Number: 98, Content: []any{[]byte{0xa1, 0x01, 0x26}, map[string]any{}, nil, []any{}}}
	data, err := cbor.New().Marshal(cose)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(data); got != "d8628443a10126a0f680" {
		t.Fatalf("Marshal(Tag 98) = %s", got)
	}
	var back cbor.TagValue
	if err := cbor.New().Unmarshal(data, &back); err != nil || back.Number != 98 {
		t.Fatalf("Unmarshal into Tag = %+v, %v", back, err)
	}
	var transparent any
	if err := cbor.New().Unmarshal(data, &transparent); err != nil {
		t.Fatal(err)
	}
	if _, isTag := transparent.(cbor.TagValue); isTag {
		t.Fatal("the registered codec must keep an unknown tag transparent in an interface")
	}
	var kept any
	if err := cbor.NewDeterministic().Unmarshal(data, &kept); err != nil {
		t.Fatal(err)
	}
	if tag, isTag := kept.(cbor.TagValue); !isTag || tag.Number != 98 {
		t.Fatalf("NewDeterministic into an interface = %#v, want a Tag 98", kept)
	}
	var keyed any
	if err := cbor.NewDeterministic().Unmarshal([]byte{0xa2, 0x30, 0x30, 0xd7, 0x80, 0x30}, &keyed); !errs.HasCode(err, corecbor.CodeCBORUnmarshalFailed) {
		t.Fatalf("a Tag holding an array as a map key = %v, want UNMARSHAL_FAILED and no panic", err)
	}
	var untagged cbor.TagValue
	if err := cbor.New().Unmarshal([]byte{0x01}, &untagged); !errs.HasCode(err, corecbor.CodeCBORUnmarshalFailed) {
		t.Fatalf("an untagged item into a Tag = %v, want UNMARSHAL_FAILED", err)
	}
	for _, bad := range []cbor.TagValue{{Number: 0, Content: 5}, {Number: 1, Content: "x"}, {Number: 2, Content: "x"}, {Number: 3, Content: nil}} {
		if _, err := cbor.New().Marshal(bad); !errs.HasCode(err, corecbor.CodeCBORMarshalFailed) {
			t.Fatalf("Marshal(%+v) = %v, want MARSHAL_FAILED", bad, err)
		}
	}
	type claims struct {
		Iss string        `cbor:"1,keyasint"`
		Tag cbor.TagValue `cbor:"61,keyasint"`
	}
	in := claims{Iss: "coap://as.example.com", Tag: cbor.TagValue{Number: 1, Content: int64(1444064944)}}
	encoded, err := cbor.NewDeterministic().Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out claims
	if err := cbor.NewDeterministic().Unmarshal(encoded, &out); err != nil || out.Iss != in.Iss || out.Tag.Number != 1 || out.Tag.Content != uint64(1444064944) {
		t.Fatalf("claims round trip = %+v, %v", out, err)
	}
}

// TestTimeOption is #896's acceptance: a time.Time with nanoseconds
// round-trips equal under the tag-1 option — integer seconds, a float when
// a double holds the instant, tag 0 when it does not — and the registered
// codec's untagged seconds are unchanged.
func TestTimeOption(t *testing.T) {
	t.Parallel()
	type stamped struct {
		T time.Time `cbor:"t,time=tag1"`
	}
	type texted struct {
		T time.Time `cbor:"t,time=tag0"`
	}
	type plain struct {
		T time.Time `cbor:"t"`
	}
	whole := time.Unix(1363896240, 0)
	half := time.Unix(1363896240, 500000000)
	nanos := time.Date(2026, 11, 3, 10, 20, 30, 123456789, time.UTC)
	for _, tc := range []struct {
		name  string
		value any
		want  string
	}{
		{"whole seconds as tag 1 integer", stamped{whole}, "a16174c11a514b67b0"},
		{"half a second as tag 1 float", stamped{half}, "a16174c1fb41d452d9ec200000"},
		{"nanoseconds as tag 0 text", stamped{nanos}, "a16174c0781e" + hex.EncodeToString([]byte("2026-11-03T10:20:30.123456789Z"))},
		{"tag 0 option", texted{time.Date(2013, 3, 21, 20, 4, 0, 0, time.UTC)}, "a16174c074323031332d30332d32315432303a30343a30305a"},
		{"zero time is null", stamped{}, "a16174f6"},
		{"no option: untagged seconds", plain{half}, "a161741a514b67b0"},
	} {
		got, err := cbor.New().Marshal(tc.value)
		if err != nil || hex.EncodeToString(got) != tc.want {
			t.Fatalf("%s: Marshal = %x, %v; want %s", tc.name, got, err, tc.want)
		}
	}
	for _, instant := range []time.Time{whole, half, nanos, time.Unix(-2, 250000000), time.Unix(0, 1)} {
		data, err := cbor.New().Marshal(stamped{instant})
		if err != nil {
			t.Fatal(err)
		}
		var back stamped
		if err := cbor.New().Unmarshal(data, &back); err != nil || !back.T.Equal(instant) {
			t.Fatalf("round trip of %v = %v, %v (%x)", instant, back.T, err, data)
		}
	}
	type misplaced struct {
		N int `cbor:"n,time=tag1"`
	}
	if _, err := cbor.New().Marshal(misplaced{}); !errs.HasCode(err, corecbor.CodeCBORMarshalFailed) {
		t.Fatalf("time option on an int = %v, want MARSHAL_FAILED", err)
	}
}
