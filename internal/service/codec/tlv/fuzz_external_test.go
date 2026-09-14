package tlv_test

import (
	"bytes"
	"errors"
	"io"
	"math"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/codec"
	"github.com/kitsunium/sdk/internal/service/codec/tlv"
)

// Wire-format bytes the seed corpus is written in terms of. The decoder's
// tag table is unexported, so the seeds restate it here: a seed that drifts
// from the real table stops being a valid starting point and the fuzzer
// spends its budget rediscovering the framing instead of the edges.
const (
	seedTagNil       byte = 0x01
	seedTagBoolFalse byte = 0x02
	seedTagBoolTrue  byte = 0x03
	seedTagInt64     byte = 0x13
	seedTagUint8     byte = 0x20
	seedTagFloat64   byte = 0x31
	seedTagString    byte = 0x40
	seedTagBytes     byte = 0x41
	seedTagSlice     byte = 0x50
	seedTagMap       byte = 0x60
	seedTagStruct    byte = 0x70
)

// Bounds the invariants assert against. maxTLVDepth is 32 in the decoder, so
// seedOverDepth nests past it on purpose; seedRecordMinBytes mirrors the
// decoder's own "tag + 1-byte length" floor and is what turns "the stream
// made no progress" into a bounded, checkable property.
const (
	seedOverDepth      int = 40
	seedRecordMinBytes int = 2
	// seedReencodeSlack bounds re-encoded size against input size. The encoder
	// canonicalises varints, so a re-encoding is normally SHORTER than the
	// bytes it came from; the slack covers the one legitimate growth path
	// (a 1-byte length re-emitted identically) plus a fixed header allowance.
	seedReencodeSlack int = 16
)

// tlvFuzzTarget is the typed struct the typed-decode target aims at. It mixes
// every family the reflection decoder dispatches on — scalar, string, bytes,
// slice, map, and a nested struct — so a fuzzed buffer can reach the typed
// field-matching path rather than stopping at the untyped map[string]any.
type tlvFuzzTarget struct {
	Name   string           `json:"name"`
	Count  int32            `json:"count"`
	Ratio  float64          `json:"ratio"`
	Blob   []byte           `json:"blob"`
	Tags   []string         `json:"tags"`
	Lookup map[string]int64 `json:"lookup"`
	Nested tlvFuzzNested    `json:"nested"`
}

// tlvFuzzNested is the inner struct of tlvFuzzTarget — one level of nesting is
// enough to exercise the recursive typed path without making the seed corpus
// unreadable.
type tlvFuzzNested struct {
	Flag  bool   `json:"flag"`
	Label string `json:"label"`
}

// tlvSeeds returns the shared seed corpus: valid records for every tag family,
// then the degenerate shapes a hand-written test tends not to cover — empty,
// truncated mid-header, a declared length far past the buffer, a declared
// element count far past the buffer, non-canonical and overflowing LEB128, and
// nesting past the depth cap.
//
// Every entry is written as raw wire bytes rather than as Marshal output: the
// mutator works on bytes, and a corpus that only ever holds encoder output
// teaches it the happy path it will never leave.
func tlvSeeds() [][]byte {
	return append(tlvValidSeeds(), tlvDegenerateSeeds()...)
}

// tlvValidSeeds returns one well-formed record per tag family.
func tlvValidSeeds() [][]byte {
	return [][]byte{
		//: zero-payload tags — the whole record is two bytes.
		{seedTagNil, 0x00},
		{seedTagBoolFalse, 0x00},
		{seedTagBoolTrue, 0x00},
		//: fixed-width numerics, big-endian payloads.
		{seedTagUint8, 0x01, 0xFF},
		{seedTagInt64, 0x08, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		//: float64 payload is a quiet NaN — the one value that defeats ==.
		{seedTagFloat64, 0x08, 0x7F, 0xF8, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01},
		//: length-prefixed scalars.
		{seedTagString, 0x03, 'a', 'b', 'c'},
		{seedTagBytes, 0x02, 0x00, 0xFF},
		//: composite: a 2-element slice whose elements are zero-payload tags.
		{seedTagSlice, 0x02, seedTagNil, 0x00, seedTagBoolTrue, 0x00},
		//: composite: a 1-pair map, key then value, each a full record.
		{seedTagMap, 0x01, seedTagString, 0x01, 'k', seedTagNil, 0x00},
		//: composite: a 1-field struct, name-TLV then value-TLV.
		{seedTagStruct, 0x01, seedTagString, 0x01, 'a', seedTagBoolFalse, 0x00},
		//: a struct whose two fields carry the SAME name — the untyped decode
		//: collapses them into one map entry, so identity round-trip cannot
		//: hold here and only the fixed-point invariant can.
		{
			seedTagStruct, 0x02,
			seedTagString, 0x01, 'a', seedTagNil, 0x00,
			seedTagString, 0x01, 'a', seedTagBoolTrue, 0x00,
		},
	}
}

// tlvDegenerateSeeds returns the malformed shapes: truncation, attacker-sized
// length and count headers, non-canonical LEB128, and over-deep nesting.
func tlvDegenerateSeeds() [][]byte {
	return [][]byte{
		//: empty input — below the two-byte record floor.
		{},
		//: a tag with no length byte at all.
		{seedTagString},
		//: declared length 0x7F against a one-byte payload.
		{seedTagString, 0x7F, 'x'},
		//: declared length ~4 GiB (5-byte LEB128) against an empty payload —
		//: the allocation-guided-by-attacker case.
		{seedTagBytes, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F},
		//: declared ELEMENT COUNT ~4 GiB against an empty body.
		{seedTagSlice, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F},
		//: declared PAIR COUNT ~4 GiB — doubles inside subRecordCount.
		{seedTagMap, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F},
		//: declared FIELD COUNT ~4 GiB.
		{seedTagStruct, 0xFF, 0xFF, 0xFF, 0xFF, 0x0F},
		//: non-canonical LEB128 — 0x80 0x00 is zero written in two bytes.
		{seedTagString, 0x80, 0x00},
		//: a 10-byte LEB128 whose final byte overflows uint64.
		{seedTagBytes, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		//: an unknown tag in the scalar band and one outside every band.
		{0x00, 0x00},
		{0xFF, 0x00},
		//: nesting past the depth cap: seedOverDepth single-element slices.
		tlvDeepSeed(),
		//: two independent records back to back — trailing bytes for Unmarshal,
		//: two successful Decode calls for the streaming decoder.
		{seedTagNil, 0x00, seedTagBoolTrue, 0x00},
	}
}

// tlvDeepSeed builds seedOverDepth nested single-element slices terminated by a
// nil record, so the buffer is legal at every level and fails only on depth.
func tlvDeepSeed() []byte {
	//: two header bytes per level plus the two-byte terminal record.
	out := make([]byte, 0, seedOverDepth*seedRecordMinBytes+seedRecordMinBytes)
	//: each level is a slice declaring exactly one element.
	for range seedOverDepth {
		//: tag then a one-byte count of 1.
		out = append(out, seedTagSlice, 0x01)
	}
	//: the innermost element is the cheapest legal record.
	return append(out, seedTagNil, 0x00)
}

// tlvEqual compares two decoded TLV values structurally.
//
// reflect.DeepEqual cannot be used here for two independent reasons, and both
// would turn a correct decoder into a fuzz failure: a float64 NaN is never
// DeepEqual to itself, and the encoder walks maps via MapRange, so a map with
// more than one entry re-encodes in a different ORDER every run. Comparing
// re-encoded BYTES has the same map problem. So the invariant is stated on
// VALUES, with NaN identified by bit pattern and maps compared as sets.
func tlvEqual(a, b any) bool {
	//: NaN first — it is the only value that is not equal to itself.
	if af, ok := a.(float64); ok {
		//: both sides must be float64 for the bit comparison to mean anything.
		bf, bok := b.(float64)
		//: identical bit patterns cover NaN, -0.0, and every ordinary double.
		return bok && math.Float64bits(af) == math.Float64bits(bf)
	}
	//: []byte is not comparable and DeepEqual distinguishes nil from empty.
	if ab, ok := a.([]byte); ok {
		//: bytes.Equal treats nil and empty as the same sequence.
		bb, bok := b.([]byte)
		//: same bytes in the same order.
		return bok && bytes.Equal(ab, bb)
	}
	//: composites recurse; everything else falls through to ==.
	return tlvEqualComposite(a, b)
}

// tlvEqualComposite handles the three composite shapes the untyped decoder
// produces — []any, map[any]any, map[string]any — and defers anything else to
// a plain comparison.
func tlvEqualComposite(a, b any) bool {
	//: slices compare elementwise, in order (slice order IS significant).
	if as, ok := a.([]any); ok {
		//: shape must match before any element is touched.
		bs, bok := b.([]any)
		//: same length and same elements pairwise.
		return bok && len(as) == len(bs) && tlvEqualSlice(as, bs)
	}
	//: maps compare as sets — encode order is deliberately unspecified.
	if am, ok := a.(map[any]any); ok {
		//: shape must match first.
		bm, bok := b.(map[any]any)
		//: same cardinality and same key→value association.
		return bok && len(am) == len(bm) && tlvEqualAnyMap(am, bm)
	}
	//: struct records decode to map[string]any — same set semantics.
	if am, ok := a.(map[string]any); ok {
		//: shape must match first.
		bm, bok := b.(map[string]any)
		//: same cardinality and same key→value association.
		return bok && len(am) == len(bm) && tlvEqualStringMap(am, bm)
	}
	//: scalars left: nil, bool, int64, uint64, string — all comparable.
	return a == b
}

// tlvEqualSlice compares two same-length []any elementwise.
func tlvEqualSlice(as, bs []any) bool {
	//: walk in order — position is part of a slice's identity.
	for i := range as {
		//: first mismatch settles it.
		if !tlvEqual(as[i], bs[i]) {
			//: values diverge at index i.
			return false
		}
	}
	//: every element matched.
	return true
}

// tlvPair is one key→value association lifted out of a map[any]any so the
// multiset comparison can walk it positionally. Named rather than anonymous so
// the helper below can take it as a parameter type.
type tlvPair struct {
	key   any
	value any
}

// tlvEqualAnyMap compares two same-length map[any]any as key→value multisets.
//
// A plain bm[k] lookup is WRONG here, and the fuzzer proved it in 21 seconds:
// the untyped decoder will build map[any]any{NaN: v} from a tagMap record whose
// key is a float64 NaN, and NaN is not equal to itself under Go's map equality,
// so that entry can never be retrieved by ANY lookup — bm[NaN] misses even on
// the very map that holds it. Comparing by lookup therefore reported two
// identical maps as different. The pairwise scan below is strictly more
// correct than the lookup it replaces, not more permissive: it still demands
// equal cardinality and a total one-to-one matching.
//
// The NaN key itself is a real property of this wire format, not a fuzz
// artefact: tagFloat64 accepts any 8 bytes, so a peer can plant a map entry
// that the receiving Go program can enumerate but never look up.
func tlvEqualAnyMap(am, bm map[any]any) bool {
	//: leftovers holds only the entries a direct lookup cannot settle. In every
	//: ordinary map that is EMPTY, so the comparison stays O(n) and the
	//: pairwise fallback below is reached only by the NaN keys it exists for.
	//: Building the candidate slice unconditionally would make a decoded map of
	//: n pairs cost n² on EVERY execution, and a TLV buffer can declare a lot
	//: of pairs — the fuzzer would spend its budget inside the comparator
	//: instead of inside the parser under test.
	var leftovers []tlvPair
	//: first pass — settle every key an ordinary lookup can find.
	for ak, av := range am {
		//: a key equal to itself resolves here, in constant time.
		bv, found := bm[ak]
		//: a NaN key never finds itself; defer it to the scan.
		if !found {
			//: park the pair for the pairwise matching below.
			leftovers = append(leftovers, tlvPair{key: ak, value: av})
			//: nothing more to do for this key in this pass.
			continue
		}
		//: the key matched, so the values must match too.
		if !tlvEqual(av, bv) {
			//: same key, different value.
			return false
		}
	}
	//: the common case — nothing was deferred.
	if len(leftovers) == 0 {
		//: equal cardinality plus total inclusion means equal multisets.
		return true
	}
	//: second pass — the deferred keys must match b's own unlookupable entries.
	return tlvMatchLeftovers(leftovers, bm)
}

// tlvMatchLeftovers pairs up the entries neither map can look up — in practice
// the NaN-keyed ones — by scanning b's equally unlookupable entries and
// claiming each at most once.
func tlvMatchLeftovers(leftovers []tlvPair, bm map[any]any) bool {
	//: collect b's own unlookupable entries. A key that finds itself was
	//: already settled by the first pass and must not be claimed twice.
	entries := make([]tlvPair, 0, len(leftovers))
	//: walk b once.
	for k, v := range bm {
		//: skip every key an ordinary lookup resolves.
		if _, found := bm[k]; found {
			//: already accounted for.
			continue
		}
		//: an entry that cannot find itself is a candidate.
		entries = append(entries, tlvPair{key: k, value: v})
	}
	//: both sides must have deferred the same number of entries.
	if len(entries) != len(leftovers) {
		//: one map carries an unlookupable entry the other does not.
		return false
	}
	//: tracks which of b's entries a leftover has already claimed.
	taken := make([]bool, len(entries))
	//: every leftover must claim exactly one unclaimed entry.
	for _, pair := range leftovers {
		//: pairwise scan — this is the path NaN keys need.
		if !tlvClaimPair(entries, taken, pair.key, pair.value) {
			//: no unclaimed entry of b matches this one.
			return false
		}
	}
	//: a total matching over equal cardinalities.
	return true
}

// tlvClaimPair marks the first unclaimed entry equal to (key, value) as taken
// and reports whether one was found.
func tlvClaimPair(entries []tlvPair, taken []bool, key, value any) bool {
	//: walk b's entries looking for an unclaimed structural match.
	for i := range entries {
		//: skip entries an earlier key already claimed.
		if taken[i] {
			//: this slot is spoken for.
			continue
		}
		//: both halves of the association must match.
		if !tlvEqual(entries[i].key, key) || !tlvEqual(entries[i].value, value) {
			//: not this one.
			continue
		}
		//: claim it so no other key can reuse the same entry.
		taken[i] = true
		//: matched.
		return true
	}
	//: no unclaimed entry matched.
	return false
}

// tlvEqualStringMap compares two same-length map[string]any as key→value sets.
func tlvEqualStringMap(am, bm map[string]any) bool {
	//: every key in a must exist in b with an equal value.
	for k, av := range am {
		//: a missing key or an unequal value settles it.
		bv, found := bm[k]
		//: both conditions in one branch.
		if !found || !tlvEqual(av, bv) {
			//: association diverges at key k.
			return false
		}
	}
	//: equal cardinality plus total inclusion means equal sets.
	return true
}

// FuzzTLVUnmarshalAny drives the untyped buffered decode path.
//
// The property is CONVERGENCE, and the seed corpus is what established that it
// had to be stated that way. A one-round fixed point — Unmarshal(Marshal(v))
// == v — is FALSE for this codec, and two seeds prove it:
//
//   - a tagStruct record decodes to map[string]any, which the encoder re-emits
//     as a tagMap record (Go has no "struct" kind for a map value), so the
//     second decode yields map[any]any. The Go type moves on round one.
//   - a nil []byte / nil slice / nil map re-encodes as a zero-length record and
//     decodes back as an EMPTY non-nil value. nil and empty are the same bytes
//     on this wire.
//
// Both are defensible for a self-describing format, and both settle after one
// round. So the invariant that actually holds — and the one worth defending —
// is that the encode/decode pair REACHES a fixed point and then stays there. A
// pair that keeps moving is an interoperability bug regardless of panics: two
// peers re-serialising the same message would never agree on its bytes.
//
// The size bound is the second half: a decoder whose output can grow without
// bound relative to its input is an amplification primitive even when every
// individual field is in range.
func FuzzTLVUnmarshalAny(f *testing.F) {
	//: seed with every tag family and every degenerate header shape.
	for _, seed := range tlvSeeds() {
		//: the mutator starts from real framing rather than from noise.
		f.Add(seed)
	}
	//: one seed that is genuine encoder output, so the corpus holds at least
	//: one buffer the encoder itself considers canonical.
	if encoded, err := tlv.New().Marshal(map[string]any{"k": int64(1)}); err == nil {
		//: canonical bytes for a one-field struct record.
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := tlv.New()
		var first any
		//: a rejected buffer has no further obligations — the contract is only
		//: that the rejection is an error rather than a panic.
		if err := c.Unmarshal(data, &first); err != nil {
			//: nothing decoded; the remaining invariants do not apply.
			return
		}
		//: ACCEPTED. The encoder must be able to express what the decoder built.
		firstBytes, merr := c.Marshal(first)
		//: an inexpressible value means the two directions disagree on the type set.
		if merr != nil {
			//: report the shape, not the bytes — the corpus file holds those.
			t.Fatalf("Unmarshal accepted %d bytes and produced a %T the encoder rejects: %v",
				len(data), first, merr)
		}
		//: bounded amplification — a re-encoding may not outgrow its source.
		if len(firstBytes) > len(data)+seedReencodeSlack {
			//: growth without a matching input is an amplification primitive.
			t.Fatalf("re-encode grew %d bytes into %d (slack %d)",
				len(data), len(firstBytes), seedReencodeSlack)
		}
		//: round two settles the struct/map and nil/empty normalisations above.
		second, secondBytes := tlvRound(t, c, firstBytes)
		//: round three must change nothing — that is the convergence claim.
		third, _ := tlvRound(t, c, secondBytes)
		//: CONVERGENCE — rounds two and three must agree.
		if !tlvEqual(second, third) {
			//: the pair never settles; print both shapes for triage.
			t.Fatalf("round-trip did not converge: %T %#v became %T %#v",
				second, second, third, third)
		}
	})
}

// tlvRound decodes encoded and re-encodes the result, failing the test if
// either direction rejects bytes the other produced. Shared by the untyped
// convergence check so each round reads as one step.
func tlvRound(t *testing.T, c codec.Codec, encoded []byte) (value any, reencoded []byte) {
	t.Helper()
	var decoded any
	//: the decoder must accept its own encoder's output.
	if err := c.Unmarshal(encoded, &decoded); err != nil {
		//: round-trip is broken at the decode step.
		t.Fatalf("Unmarshal rejected the encoder's own %d bytes: %v", len(encoded), err)
	}
	out, merr := c.Marshal(decoded)
	//: and the encoder must re-express what it just round-tripped.
	if merr != nil {
		//: round-trip is broken at the encode step.
		t.Fatalf("Marshal rejected a %T its own decoder produced: %v", decoded, merr)
	}
	//: hand both halves back so the caller can chain another round.
	return decoded, out
}

// FuzzTLVUnmarshalTyped drives the reflection-typed decode path, which is a
// different body of code from the untyped one: it matches wire field names
// against struct fields and converts each value to the declared Go type
// instead of building a map[string]any.
//
// The invariant is idempotence on the target: decoding the same buffer twice
// into two fresh targets must produce two identical targets, and re-encoding
// an accepted target must yield bytes that decode to the same target again. A
// decoder that leaves partial state behind on a rejected field, or that
// depends on the zero value it was handed, fails this without panicking.
func FuzzTLVUnmarshalTyped(f *testing.F) {
	//: the degenerate corpus is the valuable half here — a typed target adds
	//: conversion failures on top of every framing failure.
	for _, seed := range tlvSeeds() {
		//: same byte-level starting points as the untyped target.
		f.Add(seed)
	}
	//: a real encoding of the exact target type, so the typed field-matching
	//: path is reachable from the corpus rather than only by chance.
	sample := tlvFuzzTarget{
		Name:   "ada",
		Count:  -3,
		Ratio:  math.Inf(-1),
		Blob:   []byte{0x00, 0xFF},
		Tags:   []string{"x", "y"},
		Lookup: map[string]int64{"a": 1},
		Nested: tlvFuzzNested{Flag: true, Label: "inner"},
	}
	//: skip the seed rather than fail setup if the encoder refuses the sample.
	if encoded, err := tlv.New().Marshal(sample); err == nil {
		//: canonical bytes for the typed target.
		f.Add(encoded)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c := tlv.New()
		var first tlvFuzzTarget
		//: a rejected buffer carries no further obligation.
		if err := c.Unmarshal(data, &first); err != nil {
			//: nothing decoded into first.
			return
		}
		//: DETERMINISM — the same bytes into a fresh target must land identically.
		var again tlvFuzzTarget
		//: the second decode must also succeed; a differing verdict is a bug.
		if err := c.Unmarshal(data, &again); err != nil {
			//: same input, different answer.
			t.Fatalf("second Unmarshal of the same %d bytes failed: %v", len(data), err)
		}
		//: a decoder that depends on hidden state fails here without panicking.
		if !tlvTargetEqual(first, again) {
			//: nondeterministic decode.
			t.Fatalf("two decodes of the same bytes differ: %#v vs %#v", first, again)
		}
		//: CONVERGENCE, for the same reason as the untyped target: a nil slice
		//: or map on the target re-encodes as a zero-length record and comes
		//: back EMPTY rather than nil, so round one legitimately moves. Round
		//: two onward must not. No size bound applies here — the bytes are
		//: dictated by the TARGET TYPE's field set, not by the input.
		second, _ := tlvTypedRound(t, c, first)
		third, _ := tlvTypedRound(t, c, second)
		//: rounds two and three must agree.
		if !tlvTargetEqual(second, third) {
			//: the pair never settles.
			t.Fatalf("typed round-trip did not converge: %#v became %#v", second, third)
		}
	})
}

// tlvTypedRound re-encodes target and decodes the bytes back into a fresh
// target, failing the test if either direction rejects what the other made.
func tlvTypedRound(t *testing.T, c codec.Codec, target tlvFuzzTarget) (next tlvFuzzTarget, encoded []byte) {
	t.Helper()
	out, merr := c.Marshal(target)
	//: the encoder must express every target the decoder can produce.
	if merr != nil {
		//: report the target for triage.
		t.Fatalf("Marshal rejected a target the decoder produced: %v (%#v)", merr, target)
	}
	//: and the decoder must accept the encoder's own bytes.
	if err := c.Unmarshal(out, &next); err != nil {
		//: round-trip broken at the decode step.
		t.Fatalf("Unmarshal rejected the encoder's own %d bytes: %v", len(out), err)
	}
	//: hand both halves back so the caller can chain another round.
	return next, out
}

// tlvTargetEqual compares two tlvFuzzTarget values with the float field
// compared by bit pattern, so a decoded NaN or -0.0 does not read as a
// spurious difference the way reflect.DeepEqual would report it.
func tlvTargetEqual(a, b tlvFuzzTarget) bool {
	//: the float field is the only one DeepEqual gets wrong.
	if math.Float64bits(a.Ratio) != math.Float64bits(b.Ratio) {
		//: different doubles, including different NaN payloads.
		return false
	}
	//: neutralise the float on both copies so DeepEqual handles the rest.
	a.Ratio, b.Ratio = 0, 0
	//: every remaining field is a string, int, slice, map, or nested struct.
	return reflect.DeepEqual(a, b)
}

// FuzzTLVDecodeStream drives the streaming decoder, which reassembles records
// from an io.Reader through a separate body of code from the buffered path:
// its own LEB128 reader, its own composite reassembly, and its own truncation
// classification. A buffer that the buffered decoder rejects can still reach a
// different branch here, and vice versa.
//
// The invariant is LIVENESS plus AGREEMENT. Liveness: every successful Decode
// must consume at least the two bytes a record's header occupies, so the
// number of records a buffer can yield is bounded by half its length — a
// decoder that returns a value without advancing the reader loops forever in
// production and is caught here as a bound violation rather than as a hang.
// Agreement: a stream carrying exactly one record must decode to the same
// value the buffered path produces from the same bytes.
func FuzzTLVDecodeStream(f *testing.F) {
	//: same corpus — the streaming path reframes the same wire bytes.
	for _, seed := range tlvSeeds() {
		//: byte-level starting points.
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		//: the streaming codec is an optional extension; skip cleanly if the
		//: TLV codec ever stops implementing it rather than panicking on nil.
		sc, ok := tlv.New().(codec.StreamingCodec)
		//: a non-streaming TLV codec is a contract change, not a fuzz finding.
		if !ok {
			//: nothing to drive.
			return
		}
		dec := sc.NewDecoder(bytes.NewReader(data))
		//: a record costs at least a tag byte and a length byte.
		maxRecords := len(data)/seedRecordMinBytes + 1
		var decoded []any
		//: drain the stream, bounded by the liveness budget.
		for range maxRecords + 1 {
			//: More() gates the loop the way a production consumer would.
			if !dec.More() {
				//: stream reports itself drained.
				break
			}
			var v any
			err := dec.Decode(&v)
			//: a clean end of stream terminates the loop.
			if errors.Is(err, io.EOF) {
				//: drained.
				break
			}
			//: any other failure halts this input — the decoder owes nothing more.
			if err != nil {
				//: malformed stream, correctly rejected.
				return
			}
			//: record the value so the count can be checked against the budget.
			decoded = append(decoded, v)
		}
		//: LIVENESS — more records than the byte budget allows means at least
		//: one Decode returned without consuming its header.
		if len(decoded) > maxRecords {
			//: zero-progress decode.
			t.Fatalf("%d bytes yielded %d records, over the %d-record budget",
				len(data), len(decoded), maxRecords)
		}
		//: AGREEMENT — a single-record stream must match the buffered decode of
		//: the same bytes, which is the only case where the two paths are
		//: reading exactly the same framing.
		if len(decoded) != 1 {
			//: zero records, or trailing records the buffered path would reject.
			return
		}
		var buffered any
		//: the buffered path rejects trailing bytes, so a disagreement here is
		//: only meaningful when it ACCEPTS; a rejection means the stream read
		//: one record out of a buffer that carries more than one.
		if err := tlv.New().Unmarshal(data, &buffered); err != nil {
			//: the two paths frame differently on this input by design.
			return
		}
		//: both paths accepted the same bytes — they must agree on the value.
		if !tlvEqual(decoded[0], buffered) {
			//: streaming and buffered decoders disagree.
			t.Fatalf("stream decoded %T %#v where the buffered path decoded %T %#v",
				decoded[0], decoded[0], buffered, buffered)
		}
	})
}
