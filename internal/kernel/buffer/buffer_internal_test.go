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
		{"pool.New returns a *[]byte at standard capacity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := pool.New()
			ptr, ok := raw.(*[]byte)
			if !ok {
				t.Fatalf("pool.New returned %T, want *[]byte", raw)
				return
			}
			if cap(*ptr) < initialCap {
				t.Errorf("pool.New cap = %d, want >= %d", cap(*ptr), initialCap)
			}
		})
	}
}
