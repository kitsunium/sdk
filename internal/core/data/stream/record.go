package stream

import "time"

// Offset is a record's position in its partition: assigned by the engine at
// [Stream.Append], consecutive within a partition from zero, and never reused
// — a trimmed offset stays trimmed and the next one keeps counting. A
// [Compactor] may leave holes between the offsets a partition still holds;
// it never renumbers them.
type Offset uint64

// RecordValue is what a producer appends: a key that places it, and the
// bytes it carries.
type RecordValue struct {
	// Key places the record: records of one key go to one partition
	// ([PartitionOf]) and are read in the order they were appended. It may
	// be empty, for a record whose order relative to others does not matter.
	Key []byte
	// Payload is the record's bytes. It may be empty; under a key, an empty
	// payload is a tombstone, the key's deletion for a [Compactor].
	Payload []byte
}

// EntryValue is one record as a partition keeps it: where it is, when it
// arrived, and what it holds.
type EntryValue struct {
	// AppendedAt is the engine's clock when the batch holding the record was
	// appended; every record of one batch carries the same instant. It is
	// what [Seeker.OffsetAt] and retention by age read.
	AppendedAt time.Time
	// Record is the record as it was appended.
	Record RecordValue
	// Partition is the partition the record was appended to.
	Partition int
	// Offset is the record's position in its partition, unique and never reused.
	Offset Offset
}

// AppendValue is where [Stream.Append] put a batch: its partition and the
// offsets of its first and last records, the batch's records holding every
// offset between them in order.
type AppendValue struct {
	// Partition is the partition the whole batch was appended to.
	Partition int
	// First is the offset of the batch's first record.
	First Offset
	// Last is the offset of the batch's last record.
	Last Offset
}

// BoundsValue is a partition's retained range: records from First up to Next
// excluded. The partition holds none when First equals Next.
type BoundsValue struct {
	// First is the lowest offset still readable. Retention and Trim raise
	// it, compaction never moves it; nothing lowers it.
	First Offset
	// Next is the offset the partition's next record will take.
	Next Offset
}

// CompactValue is what one [Compactor.Compact] did to a partition: the
// records it looked at, those it kept and those it dropped; Kept plus
// Dropped is Scanned.
type CompactValue struct {
	// Scanned is how many retained records were at least
	// [PolicyValue.CompactAfter] old, the only ones a compaction may drop.
	Scanned int64
	// Kept is how many of them survived.
	Kept int64
	// Dropped is how many of them were removed: superseded by a later
	// record of their key, or a tombstone past its second window.
	Dropped int64
}
