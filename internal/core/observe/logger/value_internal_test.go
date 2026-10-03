package logger

import "testing"

// Test_packedBits_storage exercises the packedBits alias to confirm the
// uint64 round-trip preserves every bit pattern through the alias conversion.
func Test_packedBits_storage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		raw  uint64
	}{
		{"zero", 0},
		{"one", 1},
		{"max uint64", ^uint64(0)},
		{"alternating bits", 0xAAAAAAAAAAAAAAAA},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pb := packedBits(tc.raw)
			if uint64(pb) != tc.raw {
				t.Errorf("packedBits(%x) = %x, want %x", tc.raw, uint64(pb), tc.raw)
			}
		})
	}
}

// Test_boolOne pins the bool encoding contract so future refactors do not
// silently switch the on-the-wire representation.
func Test_boolOne(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want packedBits
	}{
		{"boolOne is 1", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if boolOne != tc.want {
				t.Errorf("boolOne = %d, want %d", boolOne, tc.want)
			}
		})
	}
}
