package stream

import (
	"strconv"

	"github.com/kitsunium/sdk/internal/kernel/errs"

	corestream "github.com/kitsunium/sdk/internal/core/data/stream"
)

// offsetField renders an offset as a field: in decimal, exactly, whatever
// its size.
func offsetField(key string, offset corestream.Offset) errs.FieldValue {
	//: uint64 does not fit Int64's field for every value.
	return errs.String(key, strconv.FormatUint(uint64(offset), 10))
}

// unknownPartition refuses a partition outside [0, partitions).
func unknownPartition(partition, partitions int) error {
	//: the partition asked and the count.
	return errs.Wrap(corestream.UnknownPartition, errs.WrapParams{},
		errs.Int("partition", partition), errs.Int("partitions", partitions))
}

// invalidBatchSize refuses an empty batch or a non-positive read size.
func invalidBatchSize(field string, value int) error {
	//: which argument, and its value.
	return errs.Wrap(corestream.InvalidBatchSize, errs.WrapParams{},
		errs.String("field", field), errs.Int("value", value))
}

// recordTooLarge refuses the batch holding record index, of size bytes.
func recordTooLarge(index int, size int64, maxRecord int) error {
	//: both sizes and the index, never the bytes.
	return errs.Wrap(corestream.RecordTooLarge, errs.WrapParams{},
		errs.Int("index", index), errs.Int64("size", size), errs.Int("max", maxRecord))
}

// partitionsMixed refuses a batch whose record index hashes to got while the
// batch's first keyed record hashed to want.
func partitionsMixed(index, want, got int) error {
	//: the record that disagreed and the two partitions.
	return errs.Wrap(corestream.PartitionsMixed, errs.WrapParams{},
		errs.Int("index", index), errs.Int("partition", want), errs.Int("other", got))
}

// offsetTrimmed refuses a read from offset, below first.
func offsetTrimmed(offset, first corestream.Offset) error {
	//: the offset asked and the first retained.
	return errs.Wrap(corestream.OffsetTrimmed, errs.WrapParams{},
		offsetField("offset", offset), offsetField("first", first))
}

// offsetAhead refuses an offset past next.
func offsetAhead(offset, next corestream.Offset) error {
	//: the offset asked and the next one the partition assigns.
	return errs.Wrap(corestream.OffsetAhead, errs.WrapParams{},
		offsetField("offset", offset), offsetField("next", next))
}

// streamFull refuses an Append of size bytes that found no room under
// maxBytes while held bytes were retained.
func streamFull(size, held, maxBytes int64) error {
	//: the three figures, so an operator sees how far behind the readers are.
	return errs.Wrap(corestream.StreamFull, errs.WrapParams{},
		errs.Int64("size", size), errs.Int64("held", held), errs.Int64("max", maxBytes))
}

// misconfigured refuses an argument with no usable value.
func misconfigured(field string) error {
	//: the field, and nothing of its value.
	return errs.Wrap(corestream.StreamMisconfigured, errs.WrapParams{},
		errs.String("field", field))
}

// unknownGroup answers a Committed for a group with no cursor on partition.
func unknownGroup(group string, partition int) error {
	//: the group and the partition asked.
	return errs.Wrap(corestream.UnknownGroup, errs.WrapParams{},
		errs.String("group", group), errs.Int("partition", partition))
}
