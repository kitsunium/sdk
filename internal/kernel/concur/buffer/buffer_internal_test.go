package buffer

import "testing"

func TestInternalConstants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		got, want int
	}{
		{"Get asks for the smallest class", initialCap, classSize(0)},
		{"the largest class is 256 KiB", classSize(classes - 1), 256 << 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.got != tc.want {
				t.Errorf("%s: got %d, want %d", tc.name, tc.got, tc.want)
			}
		})
	}
}

// Test_classFor pins the class a request is served from: the smallest whose
// size is at least the request.
func Test_classFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n, want int
	}{
		{-1, 0},
		{0, 0},
		{1, 0},
		{1024, 0},
		{1025, 1},
		{4096, 1},
		{4097, 2},
		{16 << 10, 2},
		{16<<10 + 1, 3},
		{64 << 10, 3},
		{64<<10 + 1, 4},
		{256 << 10, 4},
		{256<<10 + 1, classes},
	}
	for _, tc := range tests {
		if got := classFor(tc.n); got != tc.want {
			t.Errorf("classFor(%d) = %d, want %d", tc.n, got, tc.want)
		}
		//: the class served is never smaller than the request.
		if tc.want < classes && classSize(tc.want) < tc.n {
			t.Errorf("class %d (%d bytes) is smaller than %d", tc.want, classSize(tc.want), tc.n)
		}
	}
}

// Test_classOf pins the class a returned buffer is routed to: the largest
// whose size is at most its capacity.
func Test_classOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		capacity, want int
	}{
		{0, -1},
		{1023, -1},
		{1024, 0},
		{4095, 0},
		{4096, 1},
		{16<<10 - 1, 1},
		{16 << 10, 2},
		{70 << 10, 3},
		{128 << 10, 3},
		{256 << 10, 4},
		{1 << 30, 4},
	}
	for _, tc := range tests {
		if got := classOf(tc.capacity); got != tc.want {
			t.Errorf("classOf(%d) = %d, want %d", tc.capacity, got, tc.want)
		}
	}
}

// TestInternalPoolsMakeTheirClassSize: each class's factory makes a buffer of
// exactly its size.
func TestInternalPoolsMakeTheirClassSize(t *testing.T) {
	t.Parallel()
	for k := range classes {
		ptr := pools[k].Get()
		if ptr == nil || cap(*ptr) < classSize(k) || len(*ptr) != 0 {
			t.Errorf("class %d handed out %v, want an empty buffer of at least %d", k, ptr, classSize(k))
		}
	}
}
