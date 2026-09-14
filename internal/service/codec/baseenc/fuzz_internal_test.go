package baseenc

import (
	"bytes"
	"testing"
)

// Bounds the base-conversion fuzz targets work within. maxConvBytes (4 KiB) is
// the production cap on the RAW side of the O(n²) variants; the targets stay
// well under it so one execution cannot dominate the budget, while still
// crossing every length class the transforms branch on.
const (
	// fuzzConvMaxRaw caps the raw input the round-trip targets feed the
	// quadratic transforms. Far below maxConvBytes so a single execution
	// stays in the microseconds and the fuzzer gets exec count instead.
	fuzzConvMaxRaw int = 192
)

// fuzzRawSeeds returns the raw-byte seed corpus shared by the round-trip
// targets. Leading zero bytes are the interesting axis for base conversion:
// the magnitude arithmetic drops them, so they are carried separately as a
// zero-char prefix, and an off-by-one there silently changes the value.
func fuzzRawSeeds() [][]byte {
	return [][]byte{
		//: empty — the degenerate magnitude.
		{},
		//: a single zero byte: pure zero prefix, empty magnitude.
		{0x00},
		//: several zero bytes, which must all survive as zero chars.
		{0x00, 0x00, 0x00, 0x00},
		//: zero prefix followed by a magnitude — the mixed case.
		{0x00, 0x00, 0x01},
		//: a zero byte in the MIDDLE, which is part of the magnitude and must
		//: NOT be treated as a prefix.
		{0x01, 0x00, 0x00, 0x01},
		//: a single maximal byte.
		{0xFF},
		//: all bits set across several bytes — the widest magnitude per length.
		{0xFF, 0xFF, 0xFF, 0xFF},
		//: a trailing zero byte, which the magnitude keeps.
		{0x01, 0x00},
		//: one byte over a machine word, where the carry loop runs longest.
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		//: an odd length, so Base45's trailing single-byte group is exercised.
		{0x01, 0x02, 0x03},
		//: a length that fills whole Base45 groups exactly.
		{0x01, 0x02, 0x03, 0x04},
		//: a long run at the target's own ceiling.
		bytes.Repeat([]byte{0xAB}, fuzzConvMaxRaw),
		//: a long run of zeros at the ceiling — the widest zero prefix.
		make([]byte, fuzzConvMaxRaw),
	}
}

// FuzzBaseConvRoundTrip drives the hand-rolled base-conversion transforms that
// back Base58 and Base62.
//
// These are the only arithmetic in the codec tree that the stdlib does not
// provide: encodeBaseN divides a big-endian magnitude down by the radix,
// decodeBaseN folds it back up through mulAddWindow, and mulAddWindow grows
// its live window LEFTWARDS into a reserved prefix rather than reallocating.
// That last one carries an explicit safety claim in its doc-comment — "the
// window can never run past index 0", justified by 256^n bounding radix^n for
// any radix below 256. If that claim is ever wrong by one, `mag[start]` with
// start == -1 panics. This target is what makes the claim testable rather than
// merely asserted, because the exact-recovery property below cannot hold
// unless the window arithmetic is exact.
//
// The invariant is EXACT RECOVERY, including the leading-zero prefix, which is
// carried by a completely separate code path from the magnitude and is where a
// base-conversion implementation most often loses a byte.
func FuzzBaseConvRoundTrip(f *testing.F) {
	//: raw-byte seeds covering every zero-prefix and length class.
	for _, seed := range fuzzRawSeeds() {
		//: fuzz both radices from the same corpus by carrying a selector.
		f.Add(seed, false)
		//: the second radix over the same bytes.
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, raw []byte, useBase62 bool) {
		//: the production cap is on the caller; mirror it so one execution
		//: cannot spend the whole budget inside a quadratic transform.
		if len(raw) > fuzzConvMaxRaw {
			//: over the target's own ceiling — nothing to measure here.
			return
		}
		//: pick the alphabet from the fuzzed selector so both tables are
		//: exercised by the same corpus.
		alphabet, radix, reverse := base58Alphabet, base58Radix, &base58Reverse
		//: the Base62 table when the selector says so.
		if useBase62 {
			//: swap all three together — they must stay consistent.
			alphabet, radix, reverse = base62Alphabet, base62Radix, &base62Reverse
		}
		encoded := encodeBaseN(raw, alphabet, radix)
		//: every emitted char must be a member of the alphabet it claims.
		for i, c := range encoded {
			//: a char the reverse table does not know is not in the alphabet.
			if reverse[c] == base45NotMember {
				//: the encoder emitted something its own decoder refuses.
				t.Fatalf("encode emitted byte %#x at index %d, outside the radix-%d alphabet",
					c, i, radix)
			}
		}
		decoded, ok := decodeBaseN(encoded, reverse, radix)
		//: the decoder must accept the encoder's own output, always.
		if !ok {
			//: the two directions disagree on bytes one of them just produced.
			t.Fatalf("decode refused the encoder's own %d chars for %d raw bytes",
				len(encoded), len(raw))
		}
		//: EXACT RECOVERY — leading zeros included.
		if !bytes.Equal(decoded, raw) {
			//: report both so a lost zero byte is visible at a glance.
			t.Fatalf("radix %d round-trip: %d bytes %x became %d bytes %x",
				radix, len(raw), raw, len(decoded), decoded)
		}
	})
}

// FuzzBaseConvDecode drives decodeBaseN over arbitrary TEXT rather than over
// encoder output, which is the direction an attacker actually controls.
//
// The round-trip target above only ever feeds the decoder strings the encoder
// produced — a broad measurement that would be unanimous and blind to any
// input the encoder cannot emit. This one hands it arbitrary bytes: strings
// with characters outside the alphabet, strings far longer than any magnitude
// the encoder would produce for that length, and the all-zero-char strings
// where the magnitude window stays empty and `mag[start:]` is a zero-length
// slice at index len(mag).
//
// The invariant is CANONICALITY: a string the decoder accepts must re-encode
// to ITSELF. Base conversion with an explicit leading-zero prefix is a
// bijection, so every byte string has exactly one spelling in each alphabet.
// That is strictly stronger than idempotence, and it is the form that catches
// a decoder which maps two distinct strings onto the same bytes — the shape
// that lets a signature or a cache key be computed over one spelling and
// matched by another.
func FuzzBaseConvDecode(f *testing.F) {
	//: encoder output for every raw seed, so the corpus holds valid strings.
	for _, seed := range fuzzRawSeeds() {
		//: one entry per radix.
		f.Add(encodeBaseN(seed, base58Alphabet, base58Radix), false)
		//: and the same magnitude in the other alphabet.
		f.Add(encodeBaseN(seed, base62Alphabet, base62Radix), true)
	}
	//: strings the encoder would never emit but a caller can still submit.
	f.Add([]byte(""), false)
	//: a lone zero-char — the empty magnitude with a one-byte prefix.
	f.Add([]byte("1"), false)
	//: many zero-chars, so the prefix dominates and the window stays empty.
	f.Add([]byte("11111111"), false)
	//: a character outside the Base58 alphabet ('0' is excluded by design).
	f.Add([]byte("0"), false)
	//: the visually ambiguous chars Base58 drops, all at once.
	f.Add([]byte("0OIl"), false)
	//: non-ASCII bytes, which index the reverse table at its high end.
	f.Add([]byte{0xFF, 0x80, 0x00}, false)
	f.Fuzz(func(t *testing.T, text []byte, useBase62 bool) {
		//: the production cap is maxConvEncodedBytes on this side; stay well
		//: under it so one execution cannot dominate the budget.
		if len(text) > fuzzConvMaxRaw {
			//: over the target's own ceiling.
			return
		}
		//: pick the table from the fuzzed selector.
		alphabet, radix, reverse := base58Alphabet, base58Radix, &base58Reverse
		//: the Base62 table when the selector says so.
		if useBase62 {
			//: keep all three consistent.
			alphabet, radix, reverse = base62Alphabet, base62Radix, &base62Reverse
		}
		decoded, ok := decodeBaseN(text, reverse, radix)
		//: a refused string owes nothing more than not having panicked.
		if !ok {
			//: and a refusal must not hand back bytes anyway.
			if decoded != nil {
				//: a partial result behind a false ok is state that escaped.
				t.Fatalf("decode refused %d chars but returned %d bytes", len(text), len(decoded))
			}
			//: nothing decoded.
			return
		}
		//: ACCEPTED — CANONICALITY. Base conversion with an explicit
		//: leading-zero prefix is a bijection between byte strings and
		//: alphabet strings, so re-encoding an accepted string must reproduce
		//: it exactly. This is stronger than idempotence and is what pins the
		//: zero-prefix accounting: a decoder that folded one zero-char too
		//: many into the magnitude would still round-trip its own output, but
		//: would map two distinct strings onto the same bytes.
		if reencoded := encodeBaseN(decoded, alphabet, radix); !bytes.Equal(reencoded, text) {
			//: name both forms so the ambiguity is visible.
			t.Fatalf("radix %d is not canonical: %q and %q both decode to %x",
				radix, text, reencoded, decoded)
		}
	})
}

// FuzzBase45RoundTrip drives the RFC 9285 block transform, which is a separate
// implementation from the base-conversion one above: it works in fixed groups
// (two bytes to three chars, a trailing byte to two chars) rather than over a
// whole-buffer magnitude.
//
// Its decoder carries two range checks the encoder can never violate but an
// attacker can: a 3-char group may decode to at most 0xFFFF, and a trailing
// 2-char group to at most 0xFF. Both are reachable because the alphabet has 45
// symbols and 45³ is 91 125, comfortably above 0xFFFF — so roughly a quarter
// of all syntactically valid triples are out of range. A decoder missing those
// checks would silently truncate rather than refuse.
//
// The invariant is exact recovery in one direction and refusal-or-idempotence
// in the other.
func FuzzBase45RoundTrip(f *testing.F) {
	//: raw-byte seeds, covering odd lengths so the trailing group runs.
	for _, seed := range fuzzRawSeeds() {
		//: encode-side entry.
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		//: keep executions short; Base45 is linear but the corpus is shared.
		if len(raw) > fuzzConvMaxRaw {
			//: over the target's ceiling.
			return
		}
		encoded := encodeBase45(raw)
		//: every emitted char must belong to the RFC 9285 table.
		for i, c := range encoded {
			//: a char outside the alphabet would be unparseable by any peer.
			if base45Reverse[c] == base45NotMember {
				//: the encoder emitted something outside its own alphabet.
				t.Fatalf("encode emitted byte %#x at index %d, outside the Base45 alphabet", c, i)
			}
		}
		//: the length class the format can never produce is len%3 == 1.
		if len(encoded)%base45GroupChars == 1 {
			//: an encoding of that length cannot be decoded by anyone.
			t.Fatalf("encode produced %d chars, a length Base45 cannot express", len(encoded))
		}
		decoded, ok := decodeBase45(encoded)
		//: the decoder must accept the encoder's own output.
		if !ok {
			//: the two directions disagree.
			t.Fatalf("decode refused the encoder's own %d chars for %d raw bytes",
				len(encoded), len(raw))
		}
		//: EXACT RECOVERY.
		if !bytes.Equal(decoded, raw) {
			//: report both so a dropped tail byte is visible.
			t.Fatalf("base45 round-trip: %d bytes %x became %d bytes %x",
				len(raw), raw, len(decoded), decoded)
		}
	})
}

// FuzzBase45Decode drives decodeBase45 over arbitrary text — the direction an
// attacker controls, and the one where the two range checks live.
//
// Two invariants hold here. A BOUND the encoder's own accounting implies — a
// Base45 string of n chars decodes to exactly 2*(n/3) bytes, plus one more
// when n%3 is 2, so anything else means the group loop mis-counted and could
// expand past a buffer the caller sized. And CANONICALITY, which is what
// actually defends the two range checks: see the assertion for why the bound
// alone would not notice a dropped one.
func FuzzBase45Decode(f *testing.F) {
	//: encoder output for every raw seed.
	for _, seed := range fuzzRawSeeds() {
		//: valid strings the mutator can work outward from.
		f.Add(encodeBase45(seed))
	}
	//: the impossible length class — len%3 == 1 must always be refused.
	f.Add([]byte("0000"))
	//: a group whose value exceeds 0xFFFF: ":::" is 44 + 44*45 + 44*2025 which
	//: is 91124, far above the 65535 a byte pair can hold.
	f.Add([]byte(":::"))
	//: a trailing pair above 0xFF.
	f.Add([]byte("::"))
	//: characters outside the alphabet, including lowercase.
	f.Add([]byte("abc"))
	//: non-ASCII bytes at the high end of the reverse table.
	f.Add([]byte{0xFF, 0xFE, 0xFD})
	//: empty input.
	f.Add([]byte(""))
	f.Fuzz(func(t *testing.T, text []byte) {
		//: keep executions short.
		if len(text) > fuzzConvMaxRaw {
			//: over the target's ceiling.
			return
		}
		decoded, ok := decodeBase45(text)
		//: a refused string owes nothing further.
		if !ok {
			//: but it must not hand back bytes behind the refusal.
			if decoded != nil {
				//: partial state escaped the rejection.
				t.Fatalf("decode refused %d chars but returned %d bytes", len(text), len(decoded))
			}
			//: nothing decoded.
			return
		}
		//: ACCEPTED — the decoded length is fully determined by the input
		//: length, because Base45 is a fixed-ratio block encoding.
		want := base45PairBytes * (len(text) / base45GroupChars)
		//: a trailing 2-char group contributes exactly one more byte.
		if len(text)%base45GroupChars == base45TailChars {
			//: the odd tail byte.
			want++
		}
		//: BOUND — any other length means the group loop mis-counted.
		if len(decoded) != want {
			//: this is the shape that would overflow a caller-sized buffer.
			t.Fatalf("%d chars decoded to %d bytes, want exactly %d", len(text), len(decoded), want)
		}
		//: CANONICALITY — strictly stronger than idempotence, and the
		//: invariant that actually defends the two range checks. Base45 maps
		//: each byte pair to exactly one triple, so the map is a bijection
		//: ONTO the triples whose value is at most 0xFFFF. Re-encoding an
		//: accepted string must therefore reproduce it byte for byte. A
		//: decoder that dropped the `n > base45MaxPair` check would accept an
		//: out-of-range triple and silently truncate it — the decoded LENGTH
		//: would still be right and the value would still be idempotent, so
		//: only this equality sees it. Two distinct strings decoding to the
		//: same bytes is a canonicalisation bug: it lets a signature or a
		//: cache key be computed over one form and matched by another.
		if reencoded := encodeBase45(decoded); !bytes.Equal(reencoded, text) {
			//: name both forms so the ambiguity is visible.
			t.Fatalf("base45 is not canonical: %q and %q both decode to %x",
				text, reencoded, decoded)
		}
	})
}
