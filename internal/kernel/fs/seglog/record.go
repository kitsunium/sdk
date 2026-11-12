package seglog

import (
	"encoding/binary"
	"hash/crc32"
)

// A log is a directory of fixed-size, zero-filled segment files. Every
// record is 8-byte aligned:
//
//	length u32 | crc u32 | version u8 | kind u8 | end u8 | tag u8 |
//	batch u64 | index u64 | payload | zero padding
//
// little-endian; length counts the bytes after the crc field, padding
// excluded, and the crc is the CRC-32C of those bytes. Records are written
// in batches: every record of a batch carries its sequence number, the last
// one the end-of-batch flag, and batch k+1 is written only after batch k was
// synced, so any valid record of batch k+1 proves batch k durable. It is the
// framing of the consensus file log (ADR 0152 D4) without its term, its
// hard state and its entry fields (ADR 0230).
const (
	recHeader  int   = 8
	recFixed   int   = 20
	recVersion uint8 = 1

	// recEntry is a caller's record: its tag is the caller's kind byte.
	recEntry uint8 = 1
	// recCheckpt opens every segment (checkpoint).
	recCheckpt uint8 = 2
	// recSeal closes a segment, in a synced batch of its own; its index is
	// its own offset.
	recSeal uint8 = 3
	// recDrop records a DropBefore: its index is the log's new first.
	recDrop uint8 = 4

	// recAlign is the boundary every record starts on: recovery looks for a
	// valid record after a hole at each multiple of it, and nowhere else.
	recAlign int = 8
	// checkpointLen is a checkpoint record's payload: three uint64s.
	checkpointLen int = 24

	// sealReserve keeps room for the seal record at a segment's end.
	sealReserve int64 = 64
	// growSlack is what a segment made larger than segBytes, for a batch
	// that does not fit one, holds beyond the batch and its checkpoint.
	growSlack int64 = 1024

	// scanChunk is how far ahead of its parse recovery reads a segment: a
	// sealed one is read up to its seal and at most this much past it, not
	// to its preallocated size.
	scanChunk int64 = 1 << 20

	// maxPayload is the largest payload one record carries: its length
	// stays far inside the u32 the header holds and the int a 32-bit
	// platform indexes a batch with.
	maxPayload int = 1<<30 - recHeader - recFixed
)

// DefaultSegmentBytes is a segment's size when Open is given none: 64 MiB, the
// consensus log's.
const DefaultSegmentBytes int64 = 64 << 20

// castagnoli is the CRC-32C table every record's crc is computed with.
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// align8 rounds n up to the next record boundary.
func align8(n int) int { return (n + 7) &^ 7 }

// recordLen is the aligned length on disk of a record of n payload bytes.
func recordLen(n int) int { return align8(recHeader + recFixed + n) }

// record is one decoded record; data aliases the bytes it was parsed from.
type record struct {
	kind   uint8
	tag    uint8
	end    bool
	batch  uint64
	index  uint64
	data   []byte
	length int // aligned length on disk
}

// appendRecord encodes r at the end of dst, crc sealed and zero-padded to
// the record boundary, and returns the extended slice.
func appendRecord(dst []byte, r *record) []byte {
	start := len(dst)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(recFixed+len(r.data)))
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = append(dst, recVersion, r.kind, boolByte(r.end), r.tag)
	dst = binary.LittleEndian.AppendUint64(dst, r.batch)
	dst = binary.LittleEndian.AppendUint64(dst, r.index)
	dst = append(dst, r.data...)
	sealRecord(dst[start:])
	for (len(dst)-start)%recAlign != 0 {
		dst = append(dst, 0)
	}
	return dst
}

// boolByte is v as the record's flag byte.
func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

// sealRecord recomputes a record's crc over its body. The body's end is
// summed in uint64, where a length near 2^31 cannot wrap as an int sum does
// on 32 bits.
func sealRecord(b []byte) {
	n := binary.LittleEndian.Uint32(b)
	binary.LittleEndian.PutUint32(b[4:], crc32.Checksum(b[recHeader:uint64(recHeader)+uint64(n)], castagnoli))
}

// sealedBody is the body of the record at b[0:] when its length is in range
// and its crc holds, whatever version it carries; nil otherwise. The length
// is bounded as the u32 it is, before any int sum: on 32 bits one near 2^31
// wrapped recHeader+n negative and the slice panicked.
func sealedBody(b []byte) []byte {
	if len(b) < recHeader+recFixed {
		return nil
	}
	n := binary.LittleEndian.Uint32(b)
	if n < uint32(recFixed) || uint64(n) > uint64(len(b)-recHeader) {
		return nil
	}
	body := b[recHeader : recHeader+int(n)]
	if crc32.Checksum(body, castagnoli) != binary.LittleEndian.Uint32(b[4:]) {
		return nil
	}
	return body
}

// parseRecord decodes the record at b[0:]; ok is false for a hole: a short
// read, a zero header, a length out of range or a crc that fails — and for
// a sealed record of a version this build does not write, which recovery
// tells apart with unknownVersionAt before it calls anything a hole.
func parseRecord(b []byte) (rec record, ok bool) {
	body := sealedBody(b)
	if body == nil || body[0] != recVersion {
		return rec, false
	}
	return record{
		kind: body[1], end: body[2] != 0, tag: body[3],
		batch: binary.LittleEndian.Uint64(body[4:]), index: binary.LittleEndian.Uint64(body[12:]),
		data: body[recFixed:], length: align8(recHeader + len(body)),
	}, true
}

// checkpoint is the record that opens every segment: the index its first
// entry takes, the log's first index when it was activated, and the batch
// sequence number the segment starts with.
type checkpoint struct {
	next      uint64
	first     uint64
	nextBatch uint64
}

// encode is the checkpoint record's payload: three little-endian uint64s,
// in the order decodeCheckpoint reads them back.
func (c checkpoint) encode() []byte {
	b := make([]byte, 0, checkpointLen)
	b = binary.LittleEndian.AppendUint64(b, c.next)
	b = binary.LittleEndian.AppendUint64(b, c.first)
	return binary.LittleEndian.AppendUint64(b, c.nextBatch)
}

// decodeCheckpoint parses encode's payload; ok is false for any other
// length, which recovery reads as a damaged checkpoint.
func decodeCheckpoint(b []byte) (checkpoint, bool) {
	if len(b) != checkpointLen {
		return checkpoint{}, false
	}
	return checkpoint{
		next:      binary.LittleEndian.Uint64(b),
		first:     binary.LittleEndian.Uint64(b[8:]),
		nextBatch: binary.LittleEndian.Uint64(b[16:]),
	}, true
}

// knownKind reports whether k is a record kind this build reads.
func knownKind(k uint8) bool {
	return k == recEntry || k == recCheckpt || k == recSeal || k == recDrop
}

// allZero reports whether b holds only zero bytes.
func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
