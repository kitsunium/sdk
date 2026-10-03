package recycler_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/concur/recycler"
)

// TestPoolGetBuildsWhatTheFactoryBuilds asserts the plain pool through its
// public alias: Get hands out a value of the pool's type, built by the factory
// when the pool is empty, and takes one back with Put. Whether a later Get
// reuses it is sync.Pool's decision, so no assertion depends on it.
func TestPoolGetBuildsWhatTheFactoryBuilds(t *testing.T) {
	t.Parallel()
	built := 0
	p := recycler.NewPool(func() *[]byte {
		built++
		return new(make([]byte, 0, 64))
	})
	b := p.Get()
	if b == nil || cap(*b) != 64 || built == 0 {
		t.Fatalf("Get = %v after %d factory calls, want a 64-byte buffer from the factory", b, built)
	}
	p.Put(b)
}

// TestCappedPoolResetsWhatItKeepsAndDropsWhatGrewPastTheCap asserts both
// branches of Put: a value within the cap is reset before it is pooled, and a
// value past it is dropped untouched — never reset, because its bytes may
// already belong to someone else.
func TestCappedPoolResetsWhatItKeepsAndDropsWhatGrewPastTheCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		grow      int
		wantReset bool
	}{
		{"within the cap: reset, then pooled", 8, true},
		{"past the cap: dropped as it is", 4096, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var reset []*bytes.Buffer
			p := recycler.NewCappedPool(
				func() *bytes.Buffer { return new(bytes.Buffer) },
				func(b *bytes.Buffer) { reset = append(reset, b); b.Reset() },
				func(b *bytes.Buffer) int { return b.Cap() },
				1024,
			)
			b := p.Get()
			b.Write(make([]byte, tc.grow))
			p.Put(b)
			if (len(reset) == 1) != tc.wantReset {
				t.Errorf("Put reset %d values, want reset = %v", len(reset), tc.wantReset)
			}
			if !tc.wantReset && b.Len() != tc.grow {
				t.Errorf("a dropped buffer was touched: %d bytes left of %d", b.Len(), tc.grow)
			}
		})
	}
}

// TestTheMistakesPanicAtConstruction asserts that a nil function and a
// non-positive cap panic at the constructor, not at the first Get or Put.
func TestTheMistakesPanicAtConstruction(t *testing.T) {
	t.Parallel()
	newBuf := func() *bytes.Buffer { return new(bytes.Buffer) }
	resetBuf := func(b *bytes.Buffer) { b.Reset() }
	capOf := func(b *bytes.Buffer) int { return b.Cap() }
	tests := []struct {
		name string
		call func()
	}{
		{"a nil factory", func() { recycler.NewPool[*bytes.Buffer](nil) }},
		{"a nil reset", func() { recycler.NewCappedPool(newBuf, nil, capOf, 1) }},
		{"a nil capacity function", func() { recycler.NewCappedPool(newBuf, resetBuf, nil, 1) }},
		{"a zero cap", func() { recycler.NewCappedPool(newBuf, resetBuf, capOf, 0) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Errorf("%s did not panic", tc.name)
				}
			}()
			tc.call()
		})
	}
}

// ExampleNewCappedPool recycles buffers: one within the cap is reset before it
// is pooled again, and one that grew past the cap is dropped, never reset.
func ExampleNewCappedPool() {
	buffers := recycler.NewCappedPool(
		func() *bytes.Buffer { return new(bytes.Buffer) },
		func(b *bytes.Buffer) {
			fmt.Println("reset", b.Len(), "bytes")
			b.Reset()
		},
		func(b *bytes.Buffer) int { return b.Cap() },
		1024,
	)
	small := buffers.Get()
	small.WriteString("hello")
	buffers.Put(small) // within the cap: reset, then pooled

	big := buffers.Get()
	big.Write(make([]byte, 4096))
	buffers.Put(big) // past the cap: dropped as it is
	// Output: reset 5 bytes
}
