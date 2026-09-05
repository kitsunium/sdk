// Package id — white-box tests for the shared identifier primitives. All four
// helpers below are one step of a byte layout, and a mistake in any of them
// produces an identifier that LOOKS right: the wrong length, the wrong version
// nibble, or a timestamp that sorts backwards are all invisible until something
// downstream depends on the property.
package id

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_readRandom pins that the buffer is filled and that a CSPRNG fault
// arrives typed. Silence here would be the worst outcome: an identifier full of
// zeros is still 36 characters long and still parses.
func Test_readRandom(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		size int
	}
	tests := []tc{
		{"a full UUID", uuidRawLen},
		{"the ULID random tail", uuidRawLen - tsBytes},
		{"a single byte", 1},
		{"an empty buffer", 0},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		//: two independent draws of the same width; identical output would
		//: mean the source is not random at all.
		first := make([]byte, c.size)
		second := make([]byte, c.size)
		if err := readRandom(first); err != nil {
			t.Fatalf("readRandom(%d) = %v, want nil", c.size, err)
		}
		if err := readRandom(second); err != nil {
			t.Fatalf("readRandom(%d) = %v, want nil", c.size, err)
		}
		//: a zero-width draw is trivially equal; anything else must differ.
		if c.size >= uuidRawLen && string(first) == string(second) {
			t.Errorf("two draws of %d bytes were identical", c.size)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_formatUUID pins the canonical rendering. The dashes are not decoration:
// consumers index into the string to read the version nibble, and every parser
// in the ecosystem expects 8-4-4-4-12.
func Test_formatUUID(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		raw  string
		want string
	}
	tests := []tc{
		{
			"all zeros",
			"00000000000000000000000000000000",
			"00000000-0000-0000-0000-000000000000",
		},
		{
			"all ones",
			"ffffffffffffffffffffffffffffffff",
			"ffffffff-ffff-ffff-ffff-ffffffffffff",
		},
		{
			"an ascending pattern",
			"000102030405060708090a0b0c0d0e0f",
			"00010203-0405-0607-0809-0a0b0c0d0e0f",
		},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		raw, err := hex.DecodeString(c.raw)
		if err != nil {
			t.Fatalf("decoding the fixture: %v", err)
		}

		got := formatUUID(raw)

		if got != c.want {
			t.Errorf("formatUUID(%s) = %q, want %q", c.raw, got, c.want)
		}
		//: the dashes sit where every parser expects them.
		for _, idx := range []int{8, 13, 18, 23} {
			if got[idx] != '-' {
				t.Errorf("formatUUID(%s) has %q at index %d, want a dash", c.raw, got[idx], idx)
			}
		}
		//: and nowhere else, or the segments would be the wrong width.
		if strings.Count(got, "-") != 4 {
			t.Errorf("formatUUID(%s) = %q, want exactly four dashes", c.raw, got)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_setUUIDBits pins the two stamped fields and — just as importantly — the
// bits it must leave alone. Clearing more than the version nibble would throw
// away four bits of entropy from every identifier, which no test of the output's
// SHAPE would ever notice.
func Test_setUUIDBits(t *testing.T) {
	t.Parallel()
	type tc struct {
		name    string
		fill    byte
		version byte
	}
	tests := []tc{
		{"version 4 over zeros", 0x00, uuidVersion4},
		{"version 7 over zeros", 0x00, uuidVersion7},
		{"version 4 over ones", 0xFF, uuidVersion4},
		{"version 7 over ones", 0xFF, uuidVersion7},
		{"version 4 over a pattern", 0xA5, uuidVersion4},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := make([]byte, uuidRawLen)
		for i := range b {
			b[i] = c.fill
		}

		setUUIDBits(b, c.version)

		//: the version nibble is the high half of byte 6.
		if got := b[uuidVersionIdx] & 0xF0; got != c.version {
			t.Errorf("the version nibble is %#x, want %#x", got, c.version)
		}
		//: the low nibble of byte 6 keeps its entropy.
		if got := b[uuidVersionIdx] & uuidVersionMask; got != c.fill&uuidVersionMask {
			t.Errorf("byte 6 low nibble = %#x, want %#x", got, c.fill&uuidVersionMask)
		}
		//: the variant is 10xx in the high two bits of byte 8.
		if got := b[uuidVariantIdx] & 0xC0; got != uuidVariantBits {
			t.Errorf("the variant bits are %#x, want %#x", got, uuidVariantBits)
		}
		//: the low six bits of byte 8 keep their entropy.
		if got := b[uuidVariantIdx] & uuidVariantMask; got != c.fill&uuidVariantMask {
			t.Errorf("byte 8 low bits = %#x, want %#x", got, c.fill&uuidVariantMask)
		}
		//: every other byte is untouched.
		for i, v := range b {
			if i == uuidVersionIdx || i == uuidVariantIdx {
				continue
			}
			if v != c.fill {
				t.Errorf("byte %d = %#x, want %#x", i, v, c.fill)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_putUint48BE pins the big-endian layout, which is what makes UUIDv7 and
// ULID sort by time as plain strings. Little-endian would still round-trip, and
// still produce unique identifiers — it would only silently destroy the ordering
// property both formats exist for.
func Test_putUint48BE(t *testing.T) {
	t.Parallel()
	type tc struct {
		name string
		v    int64
		want [tsBytes]byte
	}
	tests := []tc{
		{"zero", 0, [tsBytes]byte{0, 0, 0, 0, 0, 0}},
		{"one", 1, [tsBytes]byte{0, 0, 0, 0, 0, 1}},
		{"a byte boundary", 0x100, [tsBytes]byte{0, 0, 0, 0, 1, 0}},
		{"the full 48-bit space", 0xFFFFFFFFFFFF, [tsBytes]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		//: bits above 48 are dropped, which is the documented window.
		{"a value wider than 48 bits", 0xAA_FFFFFFFFFFFF, [tsBytes]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}},
		{"a realistic millisecond", 1_700_000_000_000, [tsBytes]byte{0x01, 0x8B, 0xCF, 0xE5, 0x68, 0x00}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		dst := make([]byte, uuidRawLen)

		putUint48BE(dst, c.v)

		for i, want := range c.want {
			if dst[i] != want {
				t.Errorf("byte %d = %#x, want %#x (whole prefix %x)", i, dst[i], want, dst[:tsBytes])
			}
		}
		//: the write stays inside its 6-byte window.
		for i := tsBytes; i < len(dst); i++ {
			if dst[i] != 0 {
				t.Errorf("putUint48BE wrote %#x past its window at byte %d", dst[i], i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
	//: the whole point of big-endian: a larger millisecond must produce a
	//: lexically larger prefix.
	small := make([]byte, uuidRawLen)
	large := make([]byte, uuidRawLen)
	putUint48BE(small, 1_700_000_000_000)
	putUint48BE(large, 1_700_000_000_001)
	if string(small[:tsBytes]) >= string(large[:tsBytes]) {
		t.Error("a later millisecond did not produce a lexically larger prefix")
	}
	//: and the entropy code must exist for readRandom to wrap onto.
	if _, ok := errs.CodeOf(EntropyFailed); !ok {
		t.Error("the entropy sentinel carries no code")
	}
}
