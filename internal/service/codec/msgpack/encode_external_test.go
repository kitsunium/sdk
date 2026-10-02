package msgpack_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
	"unsafe"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/codec/msgpack"
)

// Sizes at the edge of each length form.
const (
	// edge8 is the largest 8-bit length.
	edge8 int = math.MaxUint8
	// edge16 is the largest 16-bit length.
	edge16 int = math.MaxUint16
	// fixEdge is the largest fixarray / fixmap count.
	fixEdge int = 15
)

// codeOrigin is the code of an SDK error a method returns — built at run time
// with NewRuntime, so no registry audit mistakes it for a sentinel.
const codeOrigin errs.Code = 0x00_03_07_fe

// errOrigin is an SDK error a marshal method returns, to prove origin wins.
var errOrigin = errs.NewRuntime(codeOrigin, "ORIGIN", "origin failure", "msgpack test: origin")

// Fixtures for the method hooks.
type (
	// selfEncoded writes its own MessagePack.
	selfEncoded struct {
		raw []byte
		err error
	}
	// pointerText has only a pointer-receiver MarshalText.
	pointerText struct {
		S string `msgpack:"s"`
	}
	// nilBinary returns nil from MarshalBinary.
	nilBinary struct{}
	// failingBinary returns an error from MarshalBinary.
	failingBinary struct{}
	// cycle points at itself.
	cycle struct {
		Next *cycle `msgpack:"next"`
	}
	// withFunc carries a field MessagePack cannot encode.
	withFunc struct {
		A int    `msgpack:"a"`
		F func() `msgpack:"f"`
	}
)

// MarshalMsgpack returns the configured bytes or error.
func (s selfEncoded) MarshalMsgpack() ([]byte, error) { return s.raw, s.err }

// MarshalText renders the text.
func (p *pointerText) MarshalText() ([]byte, error) { return []byte("pt:" + p.S), nil }

// MarshalBinary returns nil.
func (nilBinary) MarshalBinary() ([]byte, error) { return nil, nil }

// MarshalBinary fails with a stdlib error.
func (failingBinary) MarshalBinary() ([]byte, error) { return nil, errors.New("cannot") }

// mustMarshal encodes v or fails the test.
func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := msgpack.New().Marshal(v)
	if err != nil {
		t.Fatalf("Marshal(%T): %v", v, err)
	}
	return b
}

// boolMap builds a map of n distinct keys.
func boolMap(n int) map[int]bool {
	m := make(map[int]bool, n)
	for i := range n {
		m[i] = true
	}
	return m
}

// TestMarshalLengthForms pins the header every length family chooses at the
// edges of its forms — the long forms the golden file cannot hold.
func TestMarshalLengthForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		value  any
		prefix string
	}{
		{"str 16 at 256", strings.Repeat("x", edge8+1), "da0100"},
		{"str 16 at 65535", strings.Repeat("x", edge16), "daffff"},
		{"str 32 at 65536", strings.Repeat("x", edge16+1), "db00010000"},
		{"bin 8 at 255", make([]byte, edge8), "c4ff"},
		{"bin 16 at 256", make([]byte, edge8+1), "c50100"},
		{"bin 32 at 65536", make([]byte, edge16+1), "c600010000"},
		{"fixarray at 15", make([]bool, fixEdge), "9f"},
		{"array 16 at 65535", make([]bool, edge16), "dcffff"},
		{"array 32 at 65536", make([]bool, edge16+1), "dd00010000"},
		{"fixmap at 15", boolMap(fixEdge), "8f"},
		{"map 16 at 16", boolMap(fixEdge + 1), "de0010"},
		{"map 32 at 65536", boolMap(edge16 + 1), "df00010000"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := hex.EncodeToString(mustMarshal(t, tc.value))
			if !strings.HasPrefix(got, tc.prefix) {
				t.Fatalf("prefix %s, want %s", got[:min(len(got), 12)], tc.prefix)
			}
		})
	}
}

// TestMarshalNilAndEmpty pins the line between nil and empty.
func TestMarshalNilAndEmpty(t *testing.T) {
	t.Parallel()
	var nilIface any
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{"nil slice", []string(nil), "c0"},
		{"empty slice", []string{}, "90"},
		{"nil map", map[string]int(nil), "c0"},
		{"empty map", map[string]int{}, "80"},
		{"nil pointer", (*int)(nil), "c0"},
		{"nil interface", nilIface, "c0"},
		{"nil []any", []any(nil), "c0"},
		{"empty []any", []any{}, "90"},
		{"empty bytes", []byte{}, "c400"},
		{"nil method result", nilBinary{}, "c0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hex.EncodeToString(mustMarshal(t, tc.value)); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}

// TestMarshalRefusals covers every value the encoder refuses: each is a
// MARSHAL_FAILED, never a panic, and Append leaves dst's length untouched.
func TestMarshalRefusals(t *testing.T) {
	t.Parallel()
	loop := &cycle{}
	loop.Next = loop
	selfRef := []any{nil}
	selfRef[0] = selfRef
	var ptr unsafe.Pointer
	tests := []struct {
		name  string
		value any
	}{
		{"chan", make(chan int)},
		{"func", func() {}},
		{"complex64", complex64(1)},
		{"complex128", complex(1, 2)},
		{"unsafe pointer", ptr},
		{"func field", withFunc{A: 1}},
		{"map of chan", map[string]chan int{"c": nil}},
		{"pointer cycle", loop},
		{"interface cycle", selfRef},
		{"MarshalMsgpack error", selfEncoded{err: errors.New("no")}},
		{"MarshalMsgpack truncated", selfEncoded{raw: []byte{0x92, 0x01}}},
		{"MarshalMsgpack two values", selfEncoded{raw: []byte{0x01, 0x02}}},
		{"MarshalMsgpack empty", selfEncoded{raw: []byte{}}},
		{"MarshalBinary error", failingBinary{}},
	}
	ap, ok := msgpack.New().(codec.Appender)
	if !ok {
		t.Fatal("codec is not an Appender")
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := msgpack.New().Marshal(tc.value); !errs.HasReason(err, "MARSHAL_FAILED") {
				t.Fatalf("Marshal: want MARSHAL_FAILED, got %v", err)
			}
			dst := []byte("prefix")
			out, err := ap.Append(dst, tc.value)
			if err == nil || len(out) != len(dst) || string(out) != "prefix" {
				t.Fatalf("Append: err=%v len=%d, want an error and dst untouched", err, len(out))
			}
		})
	}
}

// TestMarshalMethodOriginWins keeps an SDK error a method returns as the
// origin of the failure.
func TestMarshalMethodOriginWins(t *testing.T) {
	t.Parallel()
	_, err := msgpack.New().Marshal(selfEncoded{err: errOrigin})
	if code, ok := errs.CodeOf(err); !ok || code != codeOrigin {
		t.Fatalf("want origin code %s, got %v", codeOrigin, err)
	}
}

// TestMarshalSelfEncoded writes MarshalMsgpack's bytes as they are.
func TestMarshalSelfEncoded(t *testing.T) {
	t.Parallel()
	got := mustMarshal(t, []any{selfEncoded{raw: []byte{0x92, 0x01, 0x02}}, true})
	if want := []byte{0x92, 0x92, 0x01, 0x02, 0xc3}; !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

// TestMarshalPointerMethodOnCopy calls a pointer-receiver method on a value
// passed directly, so a value encodes the same with or without an address.
func TestMarshalPointerMethodOnCopy(t *testing.T) {
	t.Parallel()
	direct := mustMarshal(t, pointerText{S: "x"})
	viaPointer := mustMarshal(t, &pointerText{S: "x"})
	inSlice := mustMarshal(t, []pointerText{{S: "x"}})
	if !bytes.Equal(direct, viaPointer) || !bytes.Equal(inSlice[1:], direct) {
		t.Fatalf("direct %x, pointer %x, in slice %x", direct, viaPointer, inSlice)
	}
	if want := "c40470743a78"; hex.EncodeToString(direct) != want {
		t.Fatalf("got %x, want %s", direct, want)
	}
}

// TestMarshalFloat32Bits writes an untyped float32 bit for bit.
func TestMarshalFloat32Bits(t *testing.T) {
	t.Parallel()
	const signalling uint32 = 0x7fa00001
	got := mustMarshal(t, math.Float32frombits(signalling))
	if want := []byte{0xca, 0x7f, 0xa0, 0x00, 0x01}; !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

// TestMarshalDepthLimit encodes 1000 nested containers and refuses 1001 —
// and decodes back what it encodes.
func TestMarshalDepthLimit(t *testing.T) {
	t.Parallel()
	nest := func(n int) any {
		var v any = "leaf"
		for range n {
			v = []any{v}
		}
		return v
	}
	c := msgpack.New()
	deepest, err := c.Marshal(nest(1000))
	if err != nil {
		t.Fatalf("1000 levels: %v", err)
	}
	var back any
	if derr := c.Unmarshal(deepest, &back); derr != nil {
		t.Fatalf("decode of 1000 levels: %v", derr)
	}
	if _, err := c.Marshal(nest(1001)); !errs.HasReason(err, "MARSHAL_FAILED") {
		t.Fatalf("1001 levels: want MARSHAL_FAILED, got %v", err)
	}
}

// TestMarshalAppendGrowsDst appends onto a buffer with no room and onto one
// with room, keeping the prefix both times.
func TestMarshalAppendGrowsDst(t *testing.T) {
	t.Parallel()
	ap, ok := msgpack.New().(codec.Appender)
	if !ok {
		t.Fatal("codec is not an Appender")
	}
	want := mustMarshal(t, map[string]any{"k": []any{1, "two", 3.5}})
	for _, dst := range [][]byte{nil, []byte("AB"), make([]byte, 1, 512)} {
		prefix := string(dst)
		out, err := ap.Append(dst, map[string]any{"k": []any{1, "two", 3.5}})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if string(out[:len(prefix)]) != prefix || !bytes.Equal(out[len(prefix):], want) {
			t.Fatalf("Append onto %q: got %x", prefix, out)
		}
	}
}
