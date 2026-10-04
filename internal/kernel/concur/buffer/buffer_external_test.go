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
