package cbor_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/kitsunium/sdk/internal/service/data/codec/cbor"
)

// rawItem keeps the item UnmarshalCBOR is handed.
type rawItem struct {
	// Got is a copy of the item.
	Got []byte
}

// UnmarshalCBOR keeps a copy of the whole item, or fails on 0x00.
func (r *rawItem) UnmarshalCBOR(data []byte) error {
	if bytes.Equal(data, []byte{0x00}) {
		return errors.New("zero refused")
	}
	r.Got = bytes.Clone(data)
	return nil
}

// binaryItem reads its binary form.
type binaryItem struct {
	// Got is a copy of the bytes UnmarshalBinary was handed.
	Got []byte
}

// UnmarshalBinary keeps a copy of data, or fails on an empty one.
func (b *binaryItem) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		return errors.New("empty refused")
	}
	b.Got = bytes.Clone(data)
	return nil
}

// stringer is a non-empty interface the decoder writes through.
type stringer = fmt.Stringer

// named implements fmt.Stringer through a pointer.
type named struct {
	// N is decoded into.
	N int `cbor:"n"`
}

// String names the value.
func (n *named) String() string { return fmt.Sprint(n.N) }

// selfRef is a pointer type that points to itself.
type selfRef *selfRef

// TestUnmarshal_indirection pins pointers, interfaces and the types that
// decode themselves.
func TestUnmarshal_indirection(t *testing.T) {
	t.Parallel()
	seven := 7
	runTyped(t, []typedCase{
		{"pointer allocated", "01", ptrTo[*int](), nil, ""},
		{"pointer reused", "02", ptrWith(&seven), nil, ""},
		{"null is a nil pointer", "f6", ptrWith(&seven), (*int)(nil), ""},
		{"tagged null is a nil pointer", "d864f6", ptrWith(&seven), (*int)(nil), ""},
		{"undefined is a nil pointer", "f7", ptrWith(&seven), (*int)(nil), ""},
		{"pointer to pointer", "01", ptrTo[**int](), nil, ""},
		{"any is replaced", "6161", ptrWith[any](7), any("a"), ""},
		{"null sets an any to nil", "f6", ptrWith[any](7), any(nil), ""},
		{"interface holding a pointer", "a1616e03", func() any { var s stringer = &named{}; return &s }, nil, ""},
		{"nil interface with methods", "01", ptrTo[stringer](), nil, "of type fmt.Stringer"},
		{"null sets an interface to nil", "f6", func() any { var s stringer = &named{}; return &s }, stringer(nil), ""},
		{"UnmarshalCBOR sees the whole item", "d8648201f6", ptrTo[rawItem](), rawItem{Got: []byte{0xd8, 0x64, 0x82, 0x01, 0xf6}}, ""},
		{"UnmarshalCBOR sees null", "f6", ptrTo[rawItem](), rawItem{Got: []byte{0xf6}}, ""},
		{"UnmarshalCBOR failure", "00", ptrTo[rawItem](), nil, "UnmarshalCBOR failed"},
		{"UnmarshalBinary gets a byte string", "43010203", ptrTo[binaryItem](), binaryItem{Got: []byte{1, 2, 3}}, ""},
		{"UnmarshalBinary failure", "40", ptrTo[binaryItem](), nil, "UnmarshalBinary failed"},
		{"a BinaryUnmarshaler still reads a map", "a163476f744101", ptrTo[binaryItem](), binaryItem{Got: []byte{1}}, ""},
		{"url through UnmarshalBinary", "4d68747470733a2f2f612e622f63", ptrTo[url.URL](), nil, ""},
		{"channel refuses a value", "01", ptrTo[chan int](), nil, "cannot fill: chan int"},
		{"channel ignores null", "f6", ptrTo[chan int](), (chan int)(nil), ""},
		{"self-referential pointer type", "01", ptrTo[selfRef](), nil, "self-referential pointer type"},
	})
}

// TestUnmarshal_interfaceWritesThrough checks the value an interface with
// methods holds is the one written.
func TestUnmarshal_interfaceWritesThrough(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		data string
		want int
	}
	tests := []tc{{"map into the held struct", "a1616e03", 3}}
	runCase := func(t *testing.T, tc tc) {
		t.Helper()
		held := &named{}
		var s stringer = held
		if err := cbor.New().Unmarshal(mustHex(t, tc.data), &s); err != nil {
			t.Fatalf("%s: err = %v", tc.name, err)
		}
		if held.N != tc.want || s != stringer(held) {
			t.Errorf("%s: held = %+v, interface = %v", tc.name, held, s)
		}
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, tc)
		})
	}
}
