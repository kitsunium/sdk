package logger

import "testing"

func Test_newChainBuilder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		wantCapAt int
	}{
		{"factory output has the documented attrs capacity", initialAttrCap},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cb := newChainBuilder()
			//: factory must always return a non-nil pointer; the rest depends on it.
			if cb == nil {
				t.Fatal("newChainBuilder returned nil")
				return
			}
			if cap(cb.attrs) != tc.wantCapAt {
				t.Errorf("attrs cap = %d, want %d", cap(cb.attrs), tc.wantCapAt)
			}
			if len(cb.attrs) != 0 {
				t.Errorf("attrs len = %d, want 0", len(cb.attrs))
			}
		})
	}
}

func Test_initialAttrCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"initialAttrCap is positive", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if initialAttrCap != tc.want {
				t.Errorf("initialAttrCap = %d, want %d", initialAttrCap, tc.want)
			}
		})
	}
}
