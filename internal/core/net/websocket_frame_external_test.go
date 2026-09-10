// Package net_test — the differential proof for ApplyWSMask.
//
// The masking transform is the one place in this package where a defect is not
// a wrong number. A frame stream is length-prefixed, so two endpoints that
// disagree about one payload do not lose one message: they lose the stream, and
// every byte after it is read as something the sender never wrote. That is the
// failure ADR 0047 exists to prevent, and it is why the fast implementation is
// never trusted on its own — every test below compares it against
// applyWSMaskReference, which IS the byte-at-a-time code the production
// function replaced, kept here verbatim as an oracle.
package net_test

import (
	"bytes"
	"slices"
	"testing"

	corenet "github.com/kitsunium/sdk/internal/core/net"
)

// maskSweepLen is the longest payload the exhaustive sweeps cover.
//
// It is deliberately well past several word boundaries: the production function
// moves a block, then a word, then a byte, so a length has to clear at least two
// full blocks before the interaction between the three tiers is exercised at
// all. Three hundred gives nine of them and every residue class, which is the
// only way a key-rotation defect in the tail is caught rather than sampled.
const maskSweepLen int = 300

// applyWSMaskReference is the ORACLE, and it is not a paraphrase: it is the
// exact body ApplyWSMask carried before it was widened —
//
//	for i := range payload {
//		payload[i] ^= key[i&(WSMaskLen-1)]
//	}
//
// — kept in the test package so the fast path is judged against the slow one
// rather than against a second opinion written by the same hand at the same
// time. RFC 6455 §5.3 defines the transform in exactly this form, one octet at
// a time with the key index taken modulo four from the start of the payload, so
// the oracle is also the specification transcribed.
func applyWSMaskReference(payload []byte, key [corenet.WSMaskLen]byte) {
	for i := range payload {
		payload[i] ^= key[i&(corenet.WSMaskLen-1)]
	}
}

// maskTestKeys are the keys every sweep runs, and each one is present because
// it can hide a different defect.
//
// An all-zero key makes the transform the identity, so an implementation that
// wrote nothing at all would pass every test using only that key. An all-0xFF
// key is its complement and catches the opposite: a path that writes without
// XORing. A key whose four bytes are EQUAL cannot detect a rotation — every
// rotation of it is itself — which is precisely why it must never be the only
// key: the unequal keys are what make a tail resuming at the wrong index
// visible. The final entries are pseudo-random with a fixed seed, so a run is
// reproducible from its output and not merely from its source.
func maskTestKeys() [][corenet.WSMaskLen]byte {
	keys := [][corenet.WSMaskLen]byte{
		{0x00, 0x00, 0x00, 0x00},
		{0xFF, 0xFF, 0xFF, 0xFF},
		{0x5A, 0x5A, 0x5A, 0x5A},
		{0x01, 0x01, 0x01, 0x01},
		{0x37, 0xFA, 0x21, 0x3D},
		{0xDE, 0xAD, 0xBE, 0xEF},
		{0x00, 0x00, 0x00, 0x01},
		{0x01, 0x00, 0x00, 0x00},
		{0xFF, 0x00, 0x00, 0x00},
		{0x00, 0x00, 0x00, 0xFF},
	}
	//: sixteen more keys whose bytes nobody chose one by one, so the table
	//: cannot be accidentally tuned to the implementation it is judging. They
	//: are generated ARITHMETICALLY rather than drawn from math/rand: a masking
	//: key is a security context as far as the linter is concerned, crypto/rand
	//: would make a failing run unreproducible from its own output, and what
	//: this table needs is coverage of the byte space, not unpredictability.
	state := uint32(0x9E3779B9)
	for range 16 {
		var key [corenet.WSMaskLen]byte
		for i := range key {
			state = state*1664525 + 1013904223
			key[i] = byte(state >> 24)
		}
		keys = append(keys, key)
	}
	return keys
}

// maskTestPayload builds a deterministic payload of n bytes whose every byte
// differs from its neighbours, so a transposition inside a word is a failure
// rather than a coincidence.
func maskTestPayload(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i*31 + 7)
	}
	return out
}

// TestApplyWSMaskMatchesTheByteAtATimeReference is the differential gate: for
// EVERY length from 0 to maskSweepLen and every key in maskTestKeys, the
// widened implementation must produce exactly what the RFC's own one-octet-at-a-
// time form produces.
//
// # Mutation-checked
//
// Six deliberate breakages were run against this test. Five failed it and the
// sixth did not, which is the most useful of the six:
//
//   - The tail's key index off by one — `key[(index+1)&(WSMaskLen-1)]`. Failed:
//     `length 1, key 37 fa 21 3d: byte 0 = 0xfd, want 0x30 (1 total, 0 agree before it)`.
//
//   - The final partial word dropped — the byte-tail loop removed, so anything
//     the 32- and 8-byte loops could not take was left masked. Failed:
//     `length 1, key ff ff ff ff: byte 0 = 0x07, want 0xf8 (1 total, 0 agree before it)`,
//     and it also took TestApplyWSMaskDecodesTheWorkedExample down with it —
//     `unmasked = "\x7f\x9fMQX", want "Hello" (RFC 6455 §5.7)`, the RFC's own
//     five-byte example being exactly a payload with no whole word in it.
//
//   - The wide key word rotated one byte out of phase —
//     `quad = bits.RotateLeft32(quad, 8)` — so the word paths spelled the key
//     wrong while the tail stayed right. Failed:
//     `length 8, key 37 fa 21 3d: byte 0 = 0x3a, want 0x30 (8 total, 0 agree before it)`.
//     It passed for every key whose four bytes are equal, which is the whole
//     reason maskTestKeys carries unequal ones.
//
//   - Mixed byte order — the payload read and written with [binary.BigEndian]
//     while the key word stayed [binary.LittleEndian]. Failed identically at
//     length 8, on a little-endian machine, which is the point: mixing two
//     orders is wrong on EVERY host and does not wait for big-endian hardware
//     nobody here has to run it on.
//
//   - A five-byte prologue ahead of the wide loops, leaving them to start at a
//     key offset that is not a multiple of four. Failed:
//     `length 13, key 37 fa 21 3d: byte 5 = 0x95, want 0x58 (13 total, 5 agree before it)`
//     — the count naming the prologue's own length.
//
//   - AND THE ONE THAT PASSED: rewriting the tail to count its key index from
//     zero instead of from the absolute index. That is the classic
//     key-rotation defect, and in THIS shape it is not a defect at all, because
//     every stride above the tail is a multiple of WSMaskLen, so the absolute
//     index is congruent to zero when the tail begins. It is written the
//     absolute way anyway — see the comment on that loop — because the
//     alternative is a loop whose correctness depends on a fact stated three
//     loops earlier, which is exactly what the prologue mutation above breaks.

func TestApplyWSMaskMatchesTheByteAtATimeReference(t *testing.T) {
	t.Parallel()
	keys := maskTestKeys()
	for length := range maskSweepLen + 1 {
		source := maskTestPayload(length)
		//: every key against the same bytes, so a failure names the key.
		for _, key := range keys {
			want := slices.Clone(source)
			applyWSMaskReference(want, key)
			got := slices.Clone(source)
			corenet.ApplyWSMask(got, key)
			//: bytes.Equal first, because the diff walk below is only worth
			//: paying for on the run that fails.
			if !bytes.Equal(got, want) {
				t.Fatalf("length %d, key % x: %s", length, key, maskFirstDifference(got, want))
			}
		}
	}
}

// TestApplyWSMaskIsItsOwnInverse proves the property the protocol rests on: the
// same call unmasks what it masked.
//
// It is not implied by the differential test. That one proves the fast path
// agrees with the reference; this one proves the transform composed with itself
// is the identity — which is what lets a server unmask a frame with the key the
// client masked it with, and what lets the masked frame RFC 6455 prints in
// §5.7 be re-masked after it has been read. A transform that agreed with a
// BROKEN reference would pass the differential check and fail this one.
func TestApplyWSMaskIsItsOwnInverse(t *testing.T) {
	t.Parallel()
	keys := maskTestKeys()
	for length := range maskSweepLen + 1 {
		original := maskTestPayload(length)
		//: every key, because involution is a property of the pair and not of
		//: the payload.
		for _, key := range keys {
			round := slices.Clone(original)
			corenet.ApplyWSMask(round, key)
			corenet.ApplyWSMask(round, key)
			if !bytes.Equal(round, original) {
				t.Fatalf("length %d, key % x: two applications changed the payload: %s",
					length, key, maskFirstDifference(round, original))
			}
		}
	}
}

// TestApplyWSMaskStaysInsideItsWindow is the in-place contract, and it is not
// hypothetical: internal/service/net/websocket masks `c.msg[start:]`, a
// sub-slice at a non-zero offset into the reassembly buffer that already holds
// every earlier fragment of the same message. A word-at-a-time implementation
// that rounded an offset down to an alignment boundary, or that wrote a whole
// final word to cover a partial tail, would corrupt the fragment before it or
// the slack after it — and both are bytes the handler is about to be given.
//
// The window is placed at offset 7 on purpose: it is coprime with 4, 8 and 32,
// so no tier of the implementation can be accidentally aligned.
func TestApplyWSMaskStaysInsideItsWindow(t *testing.T) {
	t.Parallel()
	const offset int = 7
	const guard int = 64
	keys := maskTestKeys()
	for length := range maskSweepLen + 1 {
		//: a distinctive filler, so an out-of-window write is visible as a
		//: value rather than only as a difference.
		backing := bytes.Repeat([]byte{0xA5}, offset+length+guard)
		copy(backing[offset:offset+length], maskTestPayload(length))
		untouched := slices.Clone(backing)
		//: every key, because a stray write may depend on the key's value.
		for _, key := range keys {
			big := slices.Clone(backing)
			window := big[offset : offset+length]
			corenet.ApplyWSMask(window, key)
			want := maskTestPayload(length)
			applyWSMaskReference(want, key)
			if !bytes.Equal(window, want) {
				t.Fatalf("length %d at offset %d, key % x: %s", length, offset, key,
					maskFirstDifference(window, want))
			}
			//: the bytes BEFORE the window — a preceding fragment, in the real
			//: caller.
			if !bytes.Equal(big[:offset], untouched[:offset]) {
				t.Fatalf("length %d, key % x: the %d bytes before the window were disturbed: % x",
					length, key, offset, big[:offset])
			}
			//: the bytes AFTER it — slack the reassembly buffer has already
			//: grown but not yet filled.
			if !bytes.Equal(big[offset+length:], untouched[offset+length:]) {
				t.Fatalf("length %d, key % x: the %d bytes after the window were disturbed: % x",
					length, key, guard, big[offset+length:])
			}
		}
	}
}

// TestApplyWSMaskAcceptsAnEmptyAndANilPayload pins the two degenerate inputs a
// real connection produces. An empty Ping is the cheapest heartbeat there is
// and it reaches this function with a zero-length slice; a Close with no
// payload reaches it with a nil one. Neither may panic, and RFC 6455 gives the
// mask no header of its own that a zero length could truncate.
func TestApplyWSMaskAcceptsAnEmptyAndANilPayload(t *testing.T) {
	t.Parallel()
	key := [corenet.WSMaskLen]byte{0xDE, 0xAD, 0xBE, 0xEF}
	corenet.ApplyWSMask(nil, key)
	empty := []byte{}
	corenet.ApplyWSMask(empty, key)
	if len(empty) != 0 {
		t.Fatalf("an empty payload became %d bytes", len(empty))
	}
}

// FuzzApplyWSMask is the differential test again, over inputs nobody chose.
//
// The sweeps above are exhaustive in LENGTH but fixed in content, which is the
// right shape for a transform whose only length-dependent behaviour is which
// tier handles the tail. The fuzzer covers the other axis: arbitrary bytes and
// an arbitrary key, including keys and payloads a table author would not think
// to write. It asserts both properties at once — agreement with the reference,
// and involution — because a corpus entry that finds one is worth checking
// against the other for free.
func FuzzApplyWSMask(f *testing.F) {
	//: the RFC's own §5.7 example, plus one entry per tier of the
	//: implementation and the boundaries between them.
	f.Add([]byte{0x7f, 0x9f, 0x4d, 0x51, 0x58}, []byte{0x37, 0xfa, 0x21, 0x3d})
	f.Add([]byte(nil), []byte{0, 0, 0, 0})
	f.Add(maskTestPayload(7), []byte{0xFF, 0x00, 0xFF, 0x00})
	f.Add(maskTestPayload(8), []byte{0xDE, 0xAD, 0xBE, 0xEF})
	f.Add(maskTestPayload(31), []byte{1, 2, 3, 4})
	f.Add(maskTestPayload(32), []byte{1, 2, 3, 4})
	f.Add(maskTestPayload(33), []byte{0xAA, 0xBB, 0xCC, 0xDD})
	f.Add(maskTestPayload(125), []byte{0x5A, 0x5A, 0x5A, 0x5A})
	f.Add(maskTestPayload(4096), []byte{0x01, 0x00, 0x00, 0x00})
	f.Fuzz(func(t *testing.T, payload []byte, raw []byte) {
		var key [corenet.WSMaskLen]byte
		//: the fuzzer hands back a slice of any length; the key is a FIXED four
		//: bytes, so short input is zero-padded rather than rejected — throwing
		//: the input away would spend most of the corpus on nothing.
		copy(key[:], raw)
		want := slices.Clone(payload)
		applyWSMaskReference(want, key)
		got := slices.Clone(payload)
		corenet.ApplyWSMask(got, key)
		if !bytes.Equal(got, want) {
			t.Fatalf("length %d, key % x: %s", len(payload), key, maskFirstDifference(got, want))
		}
		//: and the property the protocol rests on, on the same input.
		corenet.ApplyWSMask(got, key)
		if !bytes.Equal(got, payload) {
			t.Fatalf("length %d, key % x: two applications changed the payload: %s",
				len(payload), key, maskFirstDifference(got, payload))
		}
	})
}

// maskFirstDifference renders the first byte at which got and want disagree,
// plus how many bytes agreed before it.
//
// The count is the diagnosis, not decoration: a mismatch at index 0 says the
// key word itself is wrong, one at a multiple of 32 says the block loop dropped
// an iteration, and one that starts only in the last seven bytes says the tail
// resumed at the wrong key index. A raw `% x` dump of two 300-byte payloads
// says none of that.
func maskFirstDifference(got, want []byte) string {
	if len(got) != len(want) {
		return "length " + itoaMask(len(got)) + ", want " + itoaMask(len(want))
	}
	for i := range got {
		//: the first disagreement is the only one worth printing.
		if got[i] != want[i] {
			return "byte " + itoaMask(i) + " = " + hexMask(got[i]) + ", want " + hexMask(want[i]) +
				" (" + itoaMask(len(got)) + " total, " + itoaMask(i) + " agree before it)"
		}
	}
	return "identical"
}

// itoaMask renders a non-negative int without pulling strconv into a file whose
// only other imports are the ones under test.
func itoaMask(v int) string {
	//: zero has no digits to emit in the loop below.
	if v == 0 {
		return "0"
	}
	var digits []byte
	for ; v > 0; v /= 10 {
		digits = append(digits, byte('0'+v%10))
	}
	slices.Reverse(digits)
	return string(digits)
}

// hexMask renders one byte as 0xNN.
func hexMask(b byte) string {
	const alphabet = "0123456789abcdef"
	return "0x" + string([]byte{alphabet[b>>4], alphabet[b&0x0F]})
}
