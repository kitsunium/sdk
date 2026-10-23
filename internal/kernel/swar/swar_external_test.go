package swar_test

import (
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/swar"
)

// ExampleIndexByte2 finds the first bare or paired line end in one pass, and
// searches for a single byte by passing it twice.
func ExampleIndexByte2() {
	fmt.Println(swar.IndexByte2([]byte("Host: x\r\nAccept: */*"), '\r', '\n'))
	fmt.Println(swar.IndexByte2([]byte("Host: x\nAccept: */*"), '\r', '\n'))
	fmt.Println(swar.IndexByte2([]byte("GET /users/7 HTTP/1.1\r\n"), '\n', '\n'))
	// Output:
	// 7
	// 7
	// 22
}

// ExampleIndexLess finds the first control byte of a header value.
func ExampleIndexLess() {
	fmt.Println(swar.IndexLess([]byte("text/plain; charset=utf-8"), 0x20))
	fmt.Println(swar.IndexLess([]byte("text/plain;\tcharset"), 0x20))
	// Output:
	// -1
	// 11
}

// ExampleSet_Index finds the first byte a quoted string must escape: a quote,
// a backslash, a control byte, or a byte that starts a multi-byte character.
func ExampleSet_Index() {
	escape := swar.NewSet('"', '\\', 0x20, true)
	fmt.Println(escape.Index([]byte("plain ascii text")))
	fmt.Println(escape.Index([]byte(`say "hi"`)))
	fmt.Println(escape.Index([]byte("tab\there")))
	fmt.Println(escape.Index([]byte("café")))
	// Output:
	// -1
	// 4
	// 3
	// 3
}

// TestBoundsOfIndexLess pins the bound's edges through the public function:
// zero holds no byte, 128 holds the ASCII range, and a bound past 128 is
// answered too, byte by byte.
func TestBoundsOfIndexLess(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		n    byte
		want int
	}{
		{"zero holds nothing", []byte{0, 1, 2}, 0, -1},
		{"one holds NUL", []byte{'a', 0}, 1, 1},
		{"128 holds 0x7F", []byte{0x80, 0xFF, 0x7F}, 128, 2},
		{"128 does not hold 0x80", []byte{0x80, 0xFF}, 128, -1},
		{"129 holds 0x80", []byte{0xFF, 0x80}, 129, 1},
		{"255 holds all but 0xFF", []byte{0xFF, 0xFE}, 255, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := swar.IndexLess(tt.in, tt.n); got != tt.want {
				t.Errorf("IndexLess(%#x, %d) = %d, want %d", tt.in, tt.n, got, tt.want)
			}
		})
	}
}

// TestSetIsAValue pins that a Set is read-only to a search: the same Set over
// the same bytes answers the same, from any number of goroutines.
func TestSetIsAValue(t *testing.T) {
	set := swar.NewSet('"', '\\', 0x20, true)
	b := []byte("0123456789abcdef0123456789abcdef0123456789\"")
	done := make(chan int)
	for range 8 {
		go func() { done <- set.Index(b) }()
	}
	for range 8 {
		if got := <-done; got != len(b)-1 {
			t.Errorf("Set.Index = %d, want %d", got, len(b)-1)
		}
	}
}
