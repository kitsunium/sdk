package cbor_test

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// emptyBinary marshals to the bytes it holds, which omitempty asks about.
type emptyBinary struct{ data []byte }

// MarshalBinary returns the held bytes.
func (e emptyBinary) MarshalBinary() ([]byte, error) { return e.data, nil }

// allOmitted is empty for omitempty when every field is.
type allOmitted struct {
	X int `cbor:"x,omitempty"`
}

// notOmitted always has a written field, so it is never empty.
type notOmitted struct {
	X int `cbor:"x"`
}

// zeroByValue declares IsZero on its value.
type zeroByValue struct{ n int }

// IsZero reports a negative n as zero, to tell the method from the default.
func (z zeroByValue) IsZero() bool { return z.n < 0 }

// zeroByPointer declares IsZero on its pointer.
type zeroByPointer struct{ n int }

// IsZero reports a negative n as zero.
func (z *zeroByPointer) IsZero() bool { return z.n < 0 }

// isZeroer is an interface type declaring IsZero.
type isZeroer interface{ IsZero() bool }

// TestMarshal_omitempty pins what omitempty omits, kind by kind: an item
// that would encode as empty — and never a time, a big.Int or a type that
// writes its own CBOR.
func TestMarshal_omitempty(t *testing.T) {
	t.Parallel()
	type omits struct {
		Bool   bool              `cbor:"bool,omitempty"`
		Int    int               `cbor:"int,omitempty"`
		Uint   uint16            `cbor:"uint,omitempty"`
		Float  float32           `cbor:"float,omitempty"`
		Text   string            `cbor:"text,omitempty"`
		Bytes  []byte            `cbor:"bytes,omitempty"`
		Array  [0]int            `cbor:"array,omitempty"`
		Slice  []int             `cbor:"slice,omitempty"`
		Map    map[string]string `cbor:"map,omitempty"`
		Ptr    *int              `cbor:"ptr,omitempty"`
		Any    any               `cbor:"any,omitempty"`
		Struct allOmitted        `cbor:"struct,omitempty"`
		Binary emptyBinary       `cbor:"binary,omitempty"`
	}
	type kept struct {
		Time    time.Time     `cbor:"time,omitempty"`
		Big     big.Int       `cbor:"big,omitempty"`
		Struct  notOmitted    `cbor:"struct,omitempty"`
		Self    selfMarshaler `cbor:"self,omitempty"`
		ToArray toArray       `cbor:"arr,omitempty"`
	}
	type tc struct {
		name string
		in   any
		want string
	}
	tests := []tc{
		{"every empty kind is omitted", omits{}, "a0"},
		{
			"non-empty kinds are written",
			omits{Uint: 2, Float: 1.5, Binary: emptyBinary{data: []byte{1}}},
			"a36475696e740265666c6f6174fa3fc000006662696e6172794101",
		},
		{
			"never-empty kinds are written",
			kept{Self: selfMarshaler{raw: []byte{0x01}}},
			"a56474696d65f6636269670066737472756374a16178006473656c660163617272830060f6",
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := cbor.New().Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: Marshal err = %v", tc.name, err)
		}
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("%s: Marshal = %x, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}

// TestMarshal_omitzero pins what omitzero omits: the type's own IsZero when
// it declares one — on the value, the pointer or an interface — and the zero
// value otherwise.
func TestMarshal_omitzero(t *testing.T) {
	t.Parallel()
	type zeros struct {
		Plain   int            `cbor:"plain,omitzero"`
		Value   zeroByValue    `cbor:"value,omitzero"`
		Address zeroByPointer  `cbor:"address,omitzero"`
		Pointer *zeroByPointer `cbor:"pointer,omitzero"`
		Iface   isZeroer       `cbor:"iface,omitzero"`
		Arr     toArray        `cbor:"arr,omitzero"`
	}
	type tc struct {
		name string
		in   zeros
		want string
	}
	tests := []tc{
		{
			"zero by each rule",
			zeros{Value: zeroByValue{n: -1}, Address: zeroByPointer{n: -1}, Iface: &zeroByPointer{n: -1}},
			"a163617272830060f6",
		},
		{
			"not zero by each rule",
			zeros{Plain: 1, Value: zeroByValue{n: 0}, Address: zeroByPointer{n: 0}, Pointer: &zeroByPointer{}, Iface: zeroByValue{n: 1}},
			"a665706c61696e016576616c7565a06761646472657373a067706f696e746572a0656966616365a063617272830060f6",
		},
		{
			"nil pointer and interface are zero",
			zeros{Value: zeroByValue{n: -1}, Address: zeroByPointer{n: -1}, Iface: (*zeroByPointer)(nil)},
			"a163617272830060f6",
		},
	}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		got, err := cbor.New().Marshal(tc.in)
		if err != nil {
			t.Fatalf("%s: Marshal err = %v", tc.name, err)
		}
		if hex.EncodeToString(got) != tc.want {
			t.Errorf("%s: Marshal = %x, want %s", tc.name, got, tc.want)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
