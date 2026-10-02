package msgpack_test

import (
	"encoding/hex"
	"reflect"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

// Struct-mapping fixtures.
type (
	// Base is an exported struct to embed.
	Base struct {
		A int `msgpack:"A"`
	}
	// withBase embeds Base; its fields inline.
	withBase struct {
		Base
		C int `msgpack:"C"`
	}
	// forcedInline inlines Base although A collides, dropping Base's A.
	forcedInline struct {
		A    string `msgpack:"A"`
		Base `msgpack:",inline"`
	}
	// duplicateKey has two fields on one key.
	duplicateKey struct {
		X int `msgpack:"x"`
		Y int `msgpack:"x"`
	}
	// embedsDuplicate inlines a type that is refused on its own.
	embedsDuplicate struct {
		duplicateKey
		Z int `msgpack:"z"`
	}
	// selfEmbed embeds a pointer to itself.
	selfEmbed struct {
		*selfEmbed
		V int `msgpack:"V"`
	}
	// hidden is unexported, so an embedded pointer to it cannot be allocated.
	hidden struct {
		H int `msgpack:"H"`
	}
	// withHidden embeds a pointer to the unexported struct.
	withHidden struct {
		*hidden
		K int `msgpack:"K"`
	}
	// level is an unexported named integer.
	level int
	// withLevel embeds the unexported integer type.
	withLevel struct {
		level
		K int `msgpack:"K"`
	}
	// arrayStruct is encoded as an array.
	arrayStruct struct {
		_msgpack struct{} `msgpack:",as_array"` //nolint:unused // read by the codec through its tag
		A        int      `msgpack:"A"`
		B        string   `msgpack:"B"`
	}
	// markerLater carries omitempty from its marker to later fields only.
	markerLater struct {
		Before   int      `msgpack:"before"`
		_msgpack struct{} `msgpack:",omitempty"` //nolint:unused // read by the codec through its tag
		After    int      `msgpack:"after"`
	}
	// zeroish has an IsZero method.
	zeroish struct {
		N int `msgpack:"n"`
	}
	// omitters puts omitempty on the IsZero type, a pointer to it, a struct
	// whose fields are all optional, and a negative zero.
	omitters struct {
		Z  zeroish   `msgpack:"z,omitempty"`
		PZ *zeroish  `msgpack:"pz,omitempty"`
		S  innerOmit `msgpack:"s,omitempty"`
		F  float64   `msgpack:"f,omitempty"`
		K  int       `msgpack:"k"`
	}
	// innerOmit is a struct every field of which is optional.
	innerOmit struct {
		Q int `msgpack:"q,omitempty"`
	}
	// aliasFirst names an alias in first position: the Go name stays.
	aliasFirst struct {
		Value int `msgpack:"alias:v"`
	}
	// jsonOnly has only a json tag, which this codec does not read.
	jsonOnly struct {
		Field int `json:"field"`
	}
)

// IsZero reports a zero N.
func (z zeroish) IsZero() bool { return z.N == 0 }

// TestStructKeys pins the key each mapping rule produces.
func TestStructKeys(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"embedded fields inline", withBase{A: 1, C: 2}, "82 a141 01 a143 02"},
		{"forced inline drops the colliding name", forcedInline{A: "s", Base: Base{A: 9}}, "81 a141 a173"},
		{"marker omitempty reaches later fields only", markerLater{}, "81 a6 6265666f7265 00"},
		{"IsZero, nil pointer, all-optional struct and -0.0 are left out", omitters{F: negZero()}, "81 a16b 00"},
		{"a non-zero IsZero type is kept", omitters{Z: zeroish{N: 1}}, "82 a17a 81a16e01 a16b 00"},
		{"alias in first position keeps the Go name", aliasFirst{Value: 3}, "81 a5 56616c7565 03"},
		{"the json tag is not read", jsonOnly{Field: 4}, "81 a5 4669656c64 04"},
		{"an unexported embedded integer is a field", withLevel{level: 5, K: 6}, "82 a5 6c6576656c 05 a14b 06"},
		{"as_array writes an array", arrayStruct{A: 1, B: "b"}, "92 01 a162"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hex.EncodeToString(mustMarshal(t, tc.value)); got != compact(tc.want) {
				t.Fatalf("got %s, want %s", got, compact(tc.want))
			}
		})
	}
}

// TestStructDecodeForms decodes structs from every form they accept.
func TestStructDecodeForms(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	t.Run("embedded struct name decodes as a nested map", func(t *testing.T) {
		var got withBase
		if err := c.Unmarshal(mustMarshal(t, map[string]any{"Base": map[string]any{"A": 7}}), &got); err != nil {
			t.Fatal(err)
		}
		if got.A != 7 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("alias decodes, the name encodes", func(t *testing.T) {
		var got aliasFirst
		if err := c.Unmarshal(mustMarshal(t, map[string]int{"v": 8}), &got); err != nil || got.Value != 8 {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})
	t.Run("as_array struct also reads the map form", func(t *testing.T) {
		var got arrayStruct
		if err := c.Unmarshal(mustMarshal(t, map[string]any{"A": 1, "B": "b"}), &got); err != nil || got.A != 1 || got.B != "b" {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})
	t.Run("a map struct also reads the array form", func(t *testing.T) {
		var got pair
		if err := c.Unmarshal(mustMarshal(t, []any{2, "x"}), &got); err != nil || got != (pair{A: 2, B: "x"}) {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})
	t.Run("an empty array is the zero struct", func(t *testing.T) {
		got := pair{A: 1}
		if err := c.Unmarshal([]byte{0x90}, &got); err != nil || got != (pair{}) {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})
	t.Run("an unexported embedded integer is stepped over", func(t *testing.T) {
		var got withLevel
		if err := c.Unmarshal(mustMarshal(t, withLevel{level: 5, K: 6}), &got); err != nil || got.level != 0 || got.K != 6 {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})
}

// TestStructRefusals covers the struct types a decode or encode refuses.
func TestStructRefusals(t *testing.T) {
	t.Parallel()
	c := msgpack.New()
	if _, err := c.Marshal(duplicateKey{}); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Fatalf("encode of a duplicate key: %v", err)
	}
	var dup duplicateKey
	if err := c.Unmarshal([]byte{0x80}, &dup); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("decode into a duplicate key: %v", err)
	}
	if _, err := c.Marshal(embedsDuplicate{}); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Fatalf("encode of an inlined duplicate key: %v", err)
	}
	var hid withHidden
	if err := c.Unmarshal(mustMarshal(t, map[string]int{"H": 1}), &hid); !errs.HasReason(err, "UNMARSHAL_FAILED") {
		t.Fatalf("decode through an unexported embedded pointer: %v", err)
	}
}

// TestStructSelfEmbedding terminates on a type that embeds a pointer to
// itself: it cannot inline, so it is one field.
func TestStructSelfEmbedding(t *testing.T) {
	t.Parallel()
	got := hex.EncodeToString(mustMarshal(t, selfEmbed{V: 1}))
	if want := compact("82 a9 73656c66456d626564 c0 a156 01"); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	var back selfEmbed
	if err := msgpack.New().Unmarshal(mustMarshal(t, selfEmbed{V: 1}), &back); err != nil || back.V != 1 {
		t.Fatalf("got %+v err=%v", back, err)
	}
}

// TestStructRoundTripsTime keeps a struct's time and pointer to time.
func TestStructRoundTripsTime(t *testing.T) {
	t.Parallel()
	when := time.Date(1969, time.July, 20, 20, 17, 40, 123, time.UTC)
	in := wireTimes{T: when, P: &when}
	var out wireTimes
	if err := msgpack.New().Unmarshal(mustMarshal(t, in), &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("got %+v, want %+v", out, in)
	}
}

// compact strips the spaces of a hex fixture.
func compact(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] != ' ' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// negZero is −0.0, which compares equal to zero and is left out.
func negZero() float64 {
	var zero float64
	return -zero
}
