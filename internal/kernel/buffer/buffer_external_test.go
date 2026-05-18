package buffer_test

import (
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/buffer"
)

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

func TestPut(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func(t *testing.T)
	}{
		{
			name: "resets length after use",
			runner: func(t *testing.T) {
				b := buffer.Get()
				*b = append(*b, "hello"...)
				buffer.Put(b)
				if len(*b) != 0 {
					t.Errorf("after Put, len = %d, want 0", len(*b))
				}
			},
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
