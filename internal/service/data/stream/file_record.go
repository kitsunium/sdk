package stream

import (
	"encoding/binary"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// The kind byte every record of a file stream carries in its segment: the
// first record of a batch is a head, every other one a tail. Recovery groups
// the records into batches by it, which is what keeps the newest batch out of
// RetainBytes' reach after a restart as before one.
const (
	kindHead uint8 = 1
	kindTail uint8 = 2
)

// stampBytes is the size of the instant every record opens with.
const stampBytes = 8

// recordOverhead bounds what the encoding adds to a record's key and
// payload: the instant and the key's length as a uvarint.
const recordOverhead = stampBytes + binary.MaxVarintLen64

// maxSegmentPayload is the largest payload kernel/fs/seglog takes in one
// record: one gibibyte less its 28-byte header. A policy whose largest record
// could not fit it is refused at construction, so an Append never fails half
// way through a batch.
const maxSegmentPayload = 1<<30 - 28

// encodeRecord appends to dst the segment payload of record, stamped at
// stamp: the instant in Unix nanoseconds, the key's length, the key, the
// payload.
func encodeRecord(dst []byte, stamp int64, record corestream.RecordValue) []byte {
	dst = binary.LittleEndian.AppendUint64(dst, uint64(stamp))
	dst = binary.AppendUvarint(dst, uint64(len(record.Key)))
	dst = append(dst, record.Key...)
	//: the payload last, so its length is what remains.
	return append(dst, record.Payload...)
}

// decodeRecord reads what encodeRecord wrote. The key and the payload alias
// payload, each capped at its end; an empty one is nil, as the memory engine
// answers. ok is false for bytes encodeRecord cannot have written.
func decodeRecord(payload []byte) (stamp int64, record corestream.RecordValue, ok bool) {
	//: the instant comes first.
	if len(payload) < stampBytes {
		//: too short to be a record.
		return 0, corestream.RecordValue{}, false
	}
	stamp = int64(binary.LittleEndian.Uint64(payload))
	rest := payload[stampBytes:]
	keyLen, n := binary.Uvarint(rest)
	//: a length that does not parse, or that runs past the record.
	if n <= 0 || keyLen > uint64(len(rest)-n) {
		//: not a record this engine wrote.
		return 0, corestream.RecordValue{}, false
	}
	end := n + int(keyLen)
	record.Key = capped(rest[n:end])
	record.Payload = capped(rest[end:])
	//: the record.
	return stamp, record, true
}

// capped returns b with its capacity cut at its length, nil when empty.
func capped(b []byte) []byte {
	//: an empty slice reads as no bytes at all.
	if len(b) == 0 {
		//: nil, as the caller's empty key reads.
		return nil
	}
	//: an append to it cannot run into the next record.
	return b[:len(b):len(b)]
}
