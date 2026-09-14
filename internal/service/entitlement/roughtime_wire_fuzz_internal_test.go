package entitlement

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// Seed-corpus shaping constants. The decoder refuses a tag count outside
// [1, roughtimeMaxTags], so a fuzzer handed four uniformly random bytes as the
// count lands in the accepted range roughly once in 10^8 tries: essentially
// never. The round-trip target therefore DERIVES a legal count from its input
// instead of hoping for one, and the byte-level target is seeded with real
// encoder output so the mutator has a valid frame to mutate AROUND rather than
// a rejection to rediscover.
const (
	// fuzzRoughtimeSeedValueLen is the value width used by the generated
	// seeds — four bytes, the unit the format counts in.
	fuzzRoughtimeSeedValueLen int = 4
	// fuzzRoughtimeMaxSplit bounds how many fields the round-trip target
	// builds, so a large blob cannot make one execution quadratic.
	fuzzRoughtimeMaxSplit int = roughtimeMaxTags
)

// fuzzRoughtimeSeedMessage builds a well-formed tagged message carrying n
// fields of four bytes each, with ascending distinct tags — the shape
// encodeRoughtimeMessage documents as its precondition.
func fuzzRoughtimeSeedMessage(n int) []byte {
	//: one field per requested tag, values distinguishable from one another.
	fields := make([]roughtimeField, 0, n)
	//: ascending tags so the encoder's "already sorted" precondition holds.
	for i := range n {
		//: a value whose bytes encode its own index, so a mis-sliced field
		//: is visible in a failure message rather than looking like any other.
		value := bytes.Repeat([]byte{byte(i)}, fuzzRoughtimeSeedValueLen)
		//: tags ascend with the index.
		fields = append(fields, roughtimeField{tag: uint32(i) + 1, value: value})
	}
	//: the encoder lays out count, offset table, tag table, then values.
	return encodeRoughtimeMessage(fields)
}

// fuzzRoughtimeSeeds returns the byte-level seed corpus: real frames of
// several sizes, then the degenerate shapes that exercise each rejection the
// header and offset-table checks implement.
func fuzzRoughtimeSeeds() [][]byte {
	seeds := [][]byte{
		//: empty, and shorter than the four-byte count itself.
		{},
		{0x01},
		{0x01, 0x00, 0x00},
		//: a count of zero — not a message.
		{0x00, 0x00, 0x00, 0x00},
		//: a count of exactly the cap, with nothing behind it.
		{byte(roughtimeMaxTags), 0x00, 0x00, 0x00},
		//: a count one past the cap.
		{byte(roughtimeMaxTags + 1), 0x00, 0x00, 0x00},
		//: a count with the high bit set — negative once narrowed to int on a
		//: 32-bit platform, enormous on a 64-bit one. Both must be refused.
		{0x00, 0x00, 0x00, 0x80},
		//: the maximum uint32 count.
		{0xFF, 0xFF, 0xFF, 0xFF},
		//: one tag declared, header short by a byte.
		{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00},
	}
	//: real frames at the interesting cardinalities: the single-field case
	//: where the offset table is EMPTY, the two-field case where it holds
	//: exactly one entry, and the frame at the cap.
	for _, n := range []int{1, 2, 3, roughtimeMaxTags} {
		//: genuine encoder output the mutator can work outward from.
		seeds = append(seeds, fuzzRoughtimeSeedMessage(n))
	}
	//: a two-field frame whose offset table DESCENDS — the case the bounds
	//: check exists for, and the one a hand-written test is least likely to
	//: think of.
	descending := fuzzRoughtimeSeedMessage(2)
	//: entry 1 of the table sits at byte 4; point it past the value area.
	binary.LittleEndian.PutUint32(descending[roughtimeTagSize:], 0xFFFFFFFF)
	//: keep it as its own corpus entry.
	seeds = append(seeds, descending)
	//: and the same frame with the offset pointing one byte past the end.
	overrun := fuzzRoughtimeSeedMessage(2)
	//: two fields of four bytes make an eight-byte value area; nine is outside.
	binary.LittleEndian.PutUint32(overrun[roughtimeTagSize:], uint32(2*fuzzRoughtimeSeedValueLen+1))
	//: keep it too.
	return append(seeds, overrun)
}

// FuzzRoughtimeDecodeMessage drives the tagged-message decoder over raw bytes.
//
// This is the most exposed parser in the SDK by provenance: the bytes arrive in
// a UDP datagram from a Roughtime server the client has not yet authenticated,
// and the message is a COUNT, an offset table, a tag table and a value area —
// every one of them chosen by the sender. The package's own doc-comment names
// the failure mode: "an off-by-one reads a field's worth of somebody else's
// memory."
//
// The invariant is TILING, which is much stronger than "it did not panic".
// The format defines each field's extent as running from its own offset to the
// NEXT field's offset, with the first offset implicit at zero and the last
// field running to the end of the buffer. That means the decoded fields must
// exactly partition the value area — no gap, no overlap, nothing outside. A
// decoder that reads past a field's end, or that lets two fields overlap, or
// that returns a slice pointing outside the packet, breaks the byte accounting
// even when every individual bounds check passes and nothing crashes.
func FuzzRoughtimeDecodeMessage(f *testing.F) {
	//: real frames plus every degenerate header shape.
	for _, seed := range fuzzRoughtimeSeeds() {
		//: the mutator starts from valid framing, not from noise.
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		fields, err := decodeRoughtimeMessage(raw)
		//: a refused message owes nothing further — the contract is that the
		//: refusal is an error rather than a panic or a bad slice.
		if err != nil {
			//: and a refusal must not hand back fields anyway.
			if fields != nil {
				//: a partial result behind an error is state that escaped.
				t.Fatalf("decode refused %d bytes but still returned %d fields", len(raw), len(fields))
			}
			//: nothing decoded.
			return
		}
		//: ACCEPTED. Recompute the header independently; it must agree, since
		//: the decoder reached its value area through this very helper.
		count, header, hdrErr := roughtimeHeaderSize(raw)
		//: a header that fails here after the decode succeeded is incoherent.
		if hdrErr != nil {
			//: the two halves of the parser disagree about the same bytes.
			t.Fatalf("decode accepted %d bytes that roughtimeHeaderSize refuses: %v", len(raw), hdrErr)
		}
		//: the value area is everything after the header the format describes.
		values := raw[header:]
		valuesLen := len(values)
		//: Re-derive the WHOLE frame from the wire, independently of the
		//: decoder: the ordered tag table and the ordered extent table. This
		//: is what closes the duplicate-tag hole. Asserting on the decoded MAP
		//: alone cannot see a mis-sliced field whose tag is later overwritten
		//: by a duplicate — the bad extent is discarded before any assertion
		//: reads it, so precisely the inputs most likely to break the slicing
		//: were the ones escaping the strongest check.
		tags, extents := fuzzRoughtimeTables(raw, count, valuesLen)
		//: TILING over every DECLARED field, not just the surviving ones.
		//: Each field ends where the next begins and the last runs to the end,
		//: so the extents must partition the value area exactly — gaps and
		//: overlaps are both unrepresentable in this framing.
		if !fuzzRoughtimeTiles(t, extents, valuesLen) {
			//: fuzzRoughtimeTiles already reported which pair broke it.
			return
		}
		//: one pass yields both the distinct count and each tag's last index.
		lastIndex := fuzzRoughtimeLastIndex(tags)
		//: the map must hold exactly one entry per DISTINCT tag.
		distinct := len(lastIndex)
		//: a field dropped for any reason other than a tag collision.
		if len(fields) != distinct {
			//: name both counts so the gap is actionable.
			t.Fatalf("header carries %d distinct tags (of %d declared) but decode returned %d fields",
				distinct, count, len(fields))
		}
		//: EXTENT AGREEMENT — every surviving entry must carry exactly the
		//: bytes its tag's LAST occurrence spans, because a later duplicate
		//: overwrites an earlier one in the map.
		for index, tag := range tags {
			//: only the last occurrence of a tag is observable in the map.
			if lastIndex[tag] != index {
				//: an earlier occurrence; the map no longer holds it.
				continue
			}
			//: the decoded bytes for this tag.
			got, found := fields[tag]
			//: the distinct-count check above already guarantees presence.
			if !found {
				//: a tag the header declares but the map lost.
				t.Fatalf("tag %#x is declared at index %d but missing from the decode", tag, index)
			}
			//: and they must be the independently derived extent, verbatim.
			want := values[extents[index].start:extents[index].end]
			//: a mismatch is the off-by-one the package doc warns about.
			if !bytes.Equal(got, want) {
				//: show both so the slip is visible.
				t.Fatalf("tag %#x at index %d decoded %d bytes %x, want the [%d,%d) extent %x",
					tag, index, len(got), got,
					extents[index].start, extents[index].end, want)
			}
		}
	})
}

// FuzzRoughtimeRoundTrip drives encode → decode over fuzzed field CONTENT.
//
// The byte-level target above almost never reaches the success path on its
// own: the tag count is the first four bytes, and a uniformly random uint32
// lands inside [1, 32] about once in 10^8 executions. Fuzzing raw bytes alone
// would therefore be a broad, unanimous and BLIND measurement — millions of
// executions all confirming the same early rejection. This target closes that
// gap by deriving a legal frame from the input, so the encoder and the decoder
// are exercised against each other on content nobody chose.
//
// The invariant is exact recovery: every field that went in must come back
// with the same tag and the same bytes, and nothing else may appear.
func FuzzRoughtimeRoundTrip(f *testing.F) {
	//: one field, empty value — the case where the offset table is absent.
	f.Add(uint8(1), []byte{})
	//: one field, four bytes.
	f.Add(uint8(1), []byte{0xDE, 0xAD, 0xBE, 0xEF})
	//: two fields over an odd-length blob, so the split is uneven.
	f.Add(uint8(2), []byte{1, 2, 3, 4, 5})
	//: the cap, with just enough bytes to go round.
	f.Add(uint8(roughtimeMaxTags), bytes.Repeat([]byte{0xA5}, roughtimeMaxTags))
	//: the cap against an empty blob — every field ends up zero-length, which
	//: makes every offset in the table identical.
	f.Add(uint8(roughtimeMaxTags), []byte{})
	//: more fields requested than the cap allows, before clamping.
	f.Add(uint8(255), []byte{0x01, 0x02})
	f.Fuzz(func(t *testing.T, rawCount uint8, blob []byte) {
		//: clamp to the range the decoder accepts — outside it the encoder
		//: would build a frame the decoder refuses by policy, which is a
		//: known asymmetry (encodeRoughtimeMessage does not enforce the cap)
		//: and not what this target is measuring.
		count := int(rawCount)%fuzzRoughtimeMaxSplit + 1
		fields := fuzzRoughtimeSplit(count, blob)
		encoded := encodeRoughtimeMessage(fields)
		decoded, err := decodeRoughtimeMessage(encoded)
		//: the decoder must accept what this encoder produced.
		if err != nil {
			//: the two directions disagree on a frame the encoder just built.
			t.Fatalf("decode refused a %d-byte frame of %d fields the encoder built: %v",
				len(encoded), count, err)
		}
		//: every field must come back, byte for byte, under its own tag.
		if len(decoded) != len(fields) {
			//: a field was lost or invented.
			t.Fatalf("encoded %d fields, decoded %d", len(fields), len(decoded))
		}
		//: compare each field against what went in.
		for _, field := range fields {
			//: the tag must be present.
			got, found := decoded[field.tag]
			//: a missing tag means the tag table was mis-read.
			if !found {
				//: name the tag so the failure is actionable.
				t.Fatalf("tag %#x did not survive the round trip", field.tag)
			}
			//: and its bytes must be exactly the ones encoded.
			if !bytes.Equal(got, field.value) {
				//: a mis-sliced value — the off-by-one the package warns about.
				t.Fatalf("tag %#x went in as %d bytes %x and came back as %d bytes %x",
					field.tag, len(field.value), field.value, len(got), got)
			}
		}
	})
}

// fuzzRoughtimeSplit cuts blob into count consecutive fields with ascending
// distinct tags, which is the precondition encodeRoughtimeMessage states. The
// last field takes whatever remains, so a blob that does not divide evenly
// still produces a legal message.
func fuzzRoughtimeSplit(count int, blob []byte) []roughtimeField {
	//: one field per requested slot.
	fields := make([]roughtimeField, 0, count)
	//: integer division; the remainder lands on the final field.
	per := len(blob) / count
	//: cursor into blob.
	at := 0
	//: cut count-1 equal pieces, then the tail.
	for i := range count {
		//: the last field absorbs the remainder so no byte is dropped.
		end := at + per
		//: final slot takes everything left.
		if i == count-1 {
			//: the tail, however long.
			end = len(blob)
		}
		//: tags ascend so the encoder's ordering precondition holds, and start
		//: at 1 so no tag is the zero value.
		fields = append(fields, roughtimeField{tag: uint32(i) + 1, value: blob[at:end]})
		//: advance past the piece just taken.
		at = end
	}
	//: a legal field list for the encoder.
	return fields
}

// fuzzRoughtimeExtent is one field's half-open span inside the value area.
type fuzzRoughtimeExtent struct {
	start int
	end   int
}

// fuzzRoughtimeTables reads the ordered tag table and the ordered extent table
// straight out of the wire.
//
// Both are derived from the FORMAT, not borrowed from the decoder: a helper
// that asked the decoder where its own tags and offsets were would agree with
// it by construction and could never contradict it. The layout is fixed — the
// count, then count-1 offsets, then count tags, then the values — so the tag
// table is the contiguous block raw[4*count : 8*count] and offset i lives at
// raw[4*i]. The caller has already had roughtimeHeaderSize confirm the buffer
// spans all of it.
func fuzzRoughtimeTables(raw []byte, count, valuesLen int) (tags []uint32, extents []fuzzRoughtimeExtent) {
	//: one slot per declared field.
	tags = make([]uint32, 0, count)
	extents = make([]fuzzRoughtimeExtent, 0, count)
	//: the tag table begins where the offset table ends.
	base := roughtimeTagSize * count
	//: walk the declared fields in wire order.
	for index := range count {
		//: this field's tag.
		tags = append(tags, binary.LittleEndian.Uint32(raw[base+roughtimeTagSize*index:]))
		//: the first value's offset is implicit at zero.
		start := 0
		//: every other start is read from the table entry before it.
		if index > 0 {
			//: offset i sits at raw[4*i].
			start = int(binary.LittleEndian.Uint32(raw[roughtimeTagSize*index:]))
		}
		//: the last value runs to the end of the buffer.
		end := valuesLen
		//: every other ends where the next one begins.
		if index+1 < count {
			//: the following table entry.
			end = int(binary.LittleEndian.Uint32(raw[roughtimeTagSize*(index+1):]))
		}
		//: record the span as the format describes it.
		extents = append(extents, fuzzRoughtimeExtent{start: start, end: end})
	}
	//: both tables, in declaration order.
	return tags, extents
}

// fuzzRoughtimeTiles reports whether the extents partition [0, valuesLen)
// exactly, failing the test with the offending pair when they do not.
func fuzzRoughtimeTiles(t *testing.T, extents []fuzzRoughtimeExtent, valuesLen int) bool {
	t.Helper()
	//: an empty table describes an empty area.
	if len(extents) == 0 {
		//: nothing to partition.
		return valuesLen == 0
	}
	//: the first field must start at the beginning of the value area.
	if extents[0].start != 0 {
		//: a leading gap the framing cannot express.
		t.Fatalf("first extent starts at %d, not 0", extents[0].start)
	}
	//: each field must end exactly where the next begins.
	for i := range len(extents) - 1 {
		//: adjacency is what "the next offset" means.
		if extents[i].end != extents[i+1].start {
			//: a gap or an overlap between two declared fields.
			t.Fatalf("extent %d ends at %d but extent %d starts at %d",
				i, extents[i].end, i+1, extents[i+1].start)
		}
	}
	//: and the last must run to the end of the buffer.
	if extents[len(extents)-1].end != valuesLen {
		//: a trailing gap.
		t.Fatalf("last extent ends at %d, not %d", extents[len(extents)-1].end, valuesLen)
	}
	//: an exact partition.
	return true
}

// fuzzRoughtimeLastIndex maps each tag to the index of its FINAL occurrence.
//
// Only the last occurrence is observable once the decoder has folded the
// fields into a map keyed by tag, because a duplicate overwrites the entry the
// earlier one wrote. Building this in one pass gives both facts the caller
// needs — how many tags are distinct (the map's length) and which index each
// surviving entry must be compared against — without a per-tag search.
func fuzzRoughtimeLastIndex(tags []uint32) map[uint32]int {
	//: one slot per distinct tag; a repeat overwrites, which is the point.
	last := make(map[uint32]int, len(tags))
	//: walk forward so the final write for a tag is its last occurrence.
	for index, tag := range tags {
		//: later index wins, mirroring what the decoder's map does.
		last[tag] = index
	}
	//: tag to final index, for every distinct tag.
	return last
}
