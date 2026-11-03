package tlv

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"slices"

	"github.com/kitsunium/sdk/internal/core/data/codec/scratch"
)

// smallMapPairs is how many pairs a map can have before the bookkeeping of
// its order leaves the stack, and before an insertion sort stops beating the
// general one (#1104).
const smallMapPairs int = 16

// prefixBytes is how many leading payload bytes of a string key are compared
// as one integer before the whole keys are.
const prefixBytes int = 8

// pairSpan locates one encoded map pair, relative to the start of the first.
type pairSpan struct {
	// prefix is a string key's first prefixBytes payload bytes, big-endian
	// and zero-padded: two string keys whose prefixes differ are ordered by
	// one integer comparison.
	prefix uint64
	// key is where the pair's key record starts.
	key int
	// value is where the pair's value record starts, and its key ends.
	value int
	// end is where the pair ends.
	end int
	// str is set when the key is a string, and prefix holds its start.
	str bool
}

// wireInt is an encoded integer key, read back for ordering: neg holds a
// negative value as its int64 bits, otherwise u is the value.
type wireInt struct {
	// u is the value, or a negative value's two's-complement bits.
	u uint64
	// neg is set for a value below zero.
	neg bool
}

// reorderPairs sorts the pairs written at b[start:] by their encoded keys and
// rewrites them in that order, in place: the region is copied once into a
// pooled scratch buffer and copied back pair by pair, so b never grows.
func reorderPairs(b []byte, start int, spans []pairSpan) []byte {
	//: zero or one pair is already in order.
	if len(spans) < 2 {
		//: nothing to move.
		return b
	}
	region := b[start:]
	sortPairs(region, spans)
	tmp := scratch.AcquireBuffer()
	tmp.Write(region)
	unsorted := tmp.Bytes()
	cursor := start
	//: the pairs, in order, over the unsorted ones.
	for i := range spans {
		cursor += copy(b[cursor:], unsorted[spans[i].key:spans[i].end])
	}
	scratch.ReleaseBuffer(tmp)
	//: same length, pairs in order.
	return b
}

// sortPairs orders spans by encoded key: an insertion sort for a small map,
// which is most of them, and the general sort beyond.
func sortPairs(region []byte, spans []pairSpan) {
	//: a large map.
	if len(spans) > smallMapPairs {
		slices.SortFunc(spans, func(x, y pairSpan) int {
			//: by encoded key.
			return comparePairs(region, &x, &y)
		})
		//: sorted.
		return
	}
	//: insert each pair among the sorted ones before it.
	for i := 1; i < len(spans); i++ {
		//: shift the larger ones right.
		for j := i; j > 0 && comparePairs(region, &spans[j-1], &spans[j]) > 0; j-- {
			spans[j-1], spans[j] = spans[j], spans[j-1]
		}
	}
}

// comparePairs orders two pairs by their keys, and two pairs whose keys
// encode alike — two NaN float keys — by their values, so even those maps
// encode one way.
func comparePairs(region []byte, x, y *pairSpan) int {
	//: two string keys whose first bytes differ: one comparison.
	if x.str && y.str && x.prefix != y.prefix {
		//: decided by the prefixes.
		return cmp.Compare(x.prefix, y.prefix)
	}
	//: the keys decide almost always.
	if c := compareEncodedKeys(region[x.key:x.value], region[y.key:y.value]); c != 0 {
		//: decided.
		return c
	}
	//: equal keys: the values decide.
	return bytes.Compare(region[x.value:x.end], region[y.value:y.end])
}

// compareEncodedKeys orders two encoded key records: two strings byte-wise
// over their payloads, as encoding/json orders keys; two integers by value,
// whatever width and signedness each was written in; anything else by its
// encoding.
func compareEncodedKeys(a, b []byte) int {
	//: two strings.
	if sa, ok := stringPayload(a); ok {
		//: both strings: their bytes.
		if sb, isStr := stringPayload(b); isStr {
			//: byte-wise.
			return bytes.Compare(sa, sb)
		}
	}
	//: two integers.
	if ia, ok := intKey(a); ok {
		//: both integers: their values.
		if ib, isInt := intKey(b); isInt {
			//: numerically.
			return ia.compare(ib)
		}
	}
	//: mixed or other kinds: the encodings, which is still one order.
	return bytes.Compare(a, b)
}

// stringPayload returns the payload of a string record, past its tag and
// length.
func stringPayload(record []byte) ([]byte, bool) {
	//: only a string record.
	if len(record) == 0 || Tag(record[0]) != tagString {
		//: not a string.
		return nil, false
	}
	_, n := binary.Uvarint(record[1:])
	//: a length the encoder wrote, so never malformed.
	if n <= 0 {
		//: not readable.
		return nil, false
	}
	//: the bytes after the header.
	return record[1+n:], true
}

// intKey reads back an integer record: tagInt8–64 signed, tagUint8–64
// unsigned, big-endian payloads of the width the tag names.
func intKey(record []byte) (wireInt, bool) {
	//: tag, one-byte length, payload.
	if len(record) < minRecordBytes {
		//: not an integer.
		return wireInt{}, false
	}
	payload := record[minRecordBytes:]
	//: one width per tag.
	switch tag := Tag(record[0]); {
	//: signed.
	case tag >= tagInt8 && tag <= tagInt64:
		u, ok := bigEndian(payload)
		//: sign-extend from the payload's width.
		shift := uint(64 - 8*len(payload))
		return signedKey(int64(u<<shift) >> shift), ok //nolint:gosec // two's complement
	//: unsigned.
	case tag >= tagUint8 && tag <= tagUint64:
		u, ok := bigEndian(payload)
		//: the value.
		return wireInt{u: u}, ok
	//: not an integer.
	default:
		return wireInt{}, false
	}
}

// bigEndian reads a 1-, 2-, 4- or 8-byte big-endian payload.
func bigEndian(payload []byte) (uint64, bool) {
	//: the widths the encoder writes.
	switch len(payload) {
	//: 8 bits.
	case width8:
		return uint64(payload[0]), true
	//: 16 bits.
	case width16:
		return uint64(binary.BigEndian.Uint16(payload)), true
	//: 32 bits.
	case width32:
		return uint64(binary.BigEndian.Uint32(payload)), true
	//: 64 bits.
	case width64:
		return binary.BigEndian.Uint64(payload), true
	//: no other width.
	default:
		return 0, false
	}
}

// signedKey places a signed value: a negative one apart, a non-negative one
// with the unsigned values it equals.
func signedKey(n int64) wireInt {
	//: below zero.
	if n < 0 {
		//: kept as its bits.
		return wireInt{u: uint64(n), neg: true} //nolint:gosec // two's complement bits, read back as int64
	}
	//: zero or above compares as unsigned.
	return wireInt{u: uint64(n)}
}

// compare orders two integer keys by value.
func (a wireInt) compare(b wireInt) int {
	//: a negative value is below every other.
	if a.neg != b.neg {
		//: the negative one first.
		if a.neg {
			//: a below b.
			return -1
		}
		//: b below a.
		return 1
	}
	//: two negatives compare as signed.
	if a.neg {
		//: signed.
		return cmp.Compare(int64(a.u), int64(b.u)) //nolint:gosec // bits written from an int64
	}
	//: two non-negatives compare as unsigned.
	return cmp.Compare(a.u, b.u)
}

// markKey records, for the key written at region[span.key:span.value],
// whether it is a string and the integer its first bytes make.
func markKey(region []byte, span *pairSpan) {
	payload, ok := stringPayload(region[span.key:span.value])
	//: only string keys are compared by prefix.
	if !ok {
		//: compared in full.
		return
	}
	var padded [prefixBytes]byte
	copy(padded[:], payload)
	span.str = true
	span.prefix = binary.BigEndian.Uint64(padded[:])
}
