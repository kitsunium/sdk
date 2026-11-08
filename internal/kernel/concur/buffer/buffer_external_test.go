package buffer_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/concur/buffer"
)

// roundTrips bounds the Put-then-Get round trips assertResetOnReuse makes
// before it concludes the pool never hands back a buffer it was just given.
// One usually suffices. A round trip misses only when the race detector's
// sync.Pool dropped the Put (one in four, at random) or another goroutine took
// the buffer first, so a thousand misses in a row do not happen by chance.
const roundTrips int = 1000

func TestGet(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		minCapGTE int
	}{
		{"fresh buffer has capacity >= 1024", 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := buffer.Get()
			if b == nil {
				t.Fatal("Get returned nil pointer")
				return
			}
			t.Cleanup(func() { buffer.Put(b) })
			if len(*b) != 0 {
				t.Errorf("fresh buffer length = %d, want 0", len(*b))
			}
			if cap(*b) < tc.minCapGTE {
				t.Errorf("fresh buffer cap = %d, want >= %d", cap(*b), tc.minCapGTE)
			}
		})
	}
}

// TestPut never touches a buffer after handing it back. Put ends the caller's
// ownership, and the pool may give the buffer at once to a parallel test whose
// own Put rewrites *b: reading len(*b) after Put raced TestGet's cleanup in
// exactly that way. The reset is observed on the buffer Get hands back.
func TestPut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func(t *testing.T)
	}{
		{
			name:   "resets length after use",
			runner: assertResetOnReuse,
		},
		{
			name: "nil pointer is safe",
			runner: func(t *testing.T) {
				buffer.Put(nil) // must not panic
			},
		},
		{
			name: "oversize buffer is dropped without panic",
			runner: func(t *testing.T) {
				buffer.Put(new(make([]byte, 0, (64*1024)+1))) // must not panic and must not be retained
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.runner(t)
		})
	}
}

// assertResetOnReuse dirties a buffer, Puts it, and Gets until the pool hands
// that same buffer back, then checks its length is zero. Comparing the two
// pointers reads neither buffer; the only buffer read is the one Get returned,
// which the test owns again.
func assertResetOnReuse(t *testing.T) {
	t.Helper()
	for range roundTrips {
		b := buffer.Get()
		*b = append(*b, "hello"...)
		buffer.Put(b)
		got := buffer.Get()
		if got == b {
			if len(*got) != 0 {
				t.Errorf("reused buffer len = %d, want 0", len(*got))
			}
			buffer.Put(got)
			return
		}
		buffer.Put(got)
	}
	t.Fatalf("the pool never handed back the buffer just Put, in %d round trips", roundTrips)
}

// TestGetSizeNeverHandsOutLess is #810's: every size is served with at least
// the capacity asked for, an empty buffer, whatever the classes hold.
func TestGetSizeNeverHandsOutLess(t *testing.T) {
	t.Parallel()
	for _, n := range []int{-5, 0, 1, 1000, 1024, 1025, 3000, 4096, 10 << 10, 16 << 10, 40 << 10, 64 << 10, 100 << 10, 256 << 10, 300 << 10} {
		for range 3 {
			b := buffer.GetSize(n)
			if len(*b) != 0 || cap(*b) < n {
				t.Errorf("GetSize(%d): len %d cap %d", n, len(*b), cap(*b))
			}
			*b = append(*b, 'x')
			buffer.Put(b)
		}
	}
}

// TestPutRetainsWithinTwiceTheClass is #810's: a 70 KiB buffer belongs to the
// 64 KiB class and is kept, as one of 128 KiB is; one of 130 KiB is dropped.
// Retention is observed on the GetSize that hands the same buffer back — a
// dropped one never comes back. The race detector's sync.Pool drops a Put at
// random, so a kept buffer is looked for over many round trips.
func TestPutRetainsWithinTwiceTheClass(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		capacity int
		kept     bool
	}{
		{"70 KiB, within twice 64 KiB", 70 << 10, true},
		{"128 KiB, exactly twice 64 KiB", 128 << 10, true},
		{"130 KiB, past twice 64 KiB", 130 << 10, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			back := false
			for range roundTrips {
				b := new(make([]byte, 0, tc.capacity))
				buffer.Put(b)
				got := buffer.GetSize(64 << 10)
				//: the pool may hand back another class member; the identity is what counts.
				if got == b {
					back = true
					break
				}
				buffer.Put(got)
			}
			if back != tc.kept {
				t.Errorf("a %d-byte buffer came back = %v, want %v", tc.capacity, back, tc.kept)
			}
		})
	}
}
