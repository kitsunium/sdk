package buffer

import "testing"

func TestInternalConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		got   int
		wantG int
	}{
		{"initialCap is positive", initialCap, 0},
		{"maxRetain is greater than initialCap", maxRetain, initialCap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got <= tc.wantG {
				t.Errorf("%s: got %d, want > %d", tc.name, tc.got, tc.wantG)
			}
		})
	}
}

func TestInternalPoolAllocatesFreshBuffer(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{"bytePool builds a *[]byte at standard capacity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ptr := bytePool.Get()
			if ptr == nil {
				t.Fatal("bytePool.Get returned nil")
				return
			}
			if cap(*ptr) < initialCap {
				t.Errorf("fresh buffer cap = %d, want >= %d", cap(*ptr), initialCap)
			}
		})
	}
}
