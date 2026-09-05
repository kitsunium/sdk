// Package id — white-box tests for the ULID generator.
package id

import (
	"strings"
	"testing"
)

// Test_ulidGen_Scheme pins the registry key. It is what a caller passes to
// id.New, so a drift here unregisters the generator from every consumer that
// asks for it by name.
func Test_ulidGen_Scheme(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		want string
	}
	tests := []tc{{"the canonical key", "ulid"}}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		if got := string(ulidGen{}.Scheme()); got != c.want {
			t.Errorf("Scheme() = %q, want %q", got, c.want)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_ulidGen_New pins the rendered shape and the ordering property. A ULID is
// chosen over a UUIDv4 precisely because it sorts by time as a plain string, so
// the prefix comparison below is the whole reason the format exists.
func Test_ulidGen_New(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		//: how many identifiers to mint; the ordering assertion needs at
		//: least two, and a larger batch also probes uniqueness.
		count int
	}
	tests := []tc{
		{"a single identifier", 1},
		{"a pair", 2},
		{"a batch", 256},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		seen := make(map[string]struct{}, c.count)
		var prev string
		for range c.count {
			got, err := ulidGen{}.New()
			if err != nil {
				t.Fatalf("New = %v, want nil", err)
			}
			//: exactly 26 Crockford characters, or no parser will accept it.
			if len(got) != ulidChars {
				t.Fatalf("New() = %q (%d chars), want %d", got, len(got), ulidChars)
			}
			for _, r := range got {
				if !strings.ContainsRune(ulidAlphabet, r) {
					t.Errorf("New() = %q contains %q, outside the Crockford alphabet", got, r)
				}
			}
			if _, dup := seen[got]; dup {
				t.Errorf("New() repeated %q", got)
			}
			seen[got] = struct{}{}

			//: the 10-character time prefix must never regress. Within one
			//: millisecond the random tail is unordered by design, which is
			//: why only the prefix is compared.
			if prev != "" && got[:10] < prev[:10] {
				t.Errorf("the time prefix regressed: %q then %q", prev, got)
			}
			prev = got
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_crockford32 pins the bit packing, including the two pad bits at the top.
//
// 26 characters carry 130 bits while the value is 128, so the encoding is
// left-padded with two zero bits. Getting that pad wrong shifts every character
// by two bits — the output still looks like a ULID, still has the right length,
// and decodes to a completely different value.
func Test_crockford32(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		in   [uuidRawLen]byte
		want string
	}
	var zeros [uuidRawLen]byte
	var ones [uuidRawLen]byte
	for i := range ones {
		ones[i] = 0xFF
	}
	//: the low bit set alone must land in the final character.
	var one [uuidRawLen]byte
	one[uuidRawLen-1] = 1
	tests := []tc{
		{"all zeros", zeros, strings.Repeat("0", ulidChars)},
		//: 128 one-bits under a 2-bit zero pad: the first character carries
		//: only 3 real bits (value 7 → 'the eighth symbol'), the rest are full.
		{"all ones", ones, "7" + strings.Repeat("Z", ulidChars-1)},
		{"the lowest bit", one, strings.Repeat("0", ulidChars-1) + "1"},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		got := crockford32(c.in[:])
		if got != c.want {
			t.Errorf("crockford32(%x) = %q, want %q", c.in, got, c.want)
		}
		if len(got) != ulidChars {
			t.Errorf("crockford32 produced %d characters, want %d", len(got), ulidChars)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the encoding must be order-preserving, which is what makes the time
	//: prefix sortable in the first place.
	var small, large [uuidRawLen]byte
	putUint48BE(small[:], 1_700_000_000_000)
	putUint48BE(large[:], 1_700_000_000_001)
	if crockford32(small[:]) >= crockford32(large[:]) {
		t.Error("a larger value did not encode to a lexically larger string")
	}
}
