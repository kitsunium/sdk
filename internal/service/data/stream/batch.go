package stream

import corestream "github.com/kitsunium/sdk/internal/core/data/stream"

// admit checks a batch before anything is written: at least one record, and
// none above maxRecord bytes, key and payload together. It returns the bytes
// the whole batch holds.
func admit(records []corestream.RecordValue, maxRecord int) (size int64, err error) {
	//: an empty batch would append nothing forever.
	if len(records) == 0 {
		//: InvalidBatchSize.
		return 0, invalidBatchSize("records", 0)
	}
	//: every record, so the refusal names the first one too large.
	for i, record := range records {
		bytes := recordBytes(record)
		//: one record above the bound refuses the whole batch.
		if bytes > int64(maxRecord) {
			//: RecordTooLarge, with both sizes and the index.
			return 0, recordTooLarge(i, bytes, maxRecord)
		}
		size += bytes
	}
	//: admissible.
	return size, nil
}

// recordBytes returns what a record counts against every bound: its key and
// payload together.
func recordBytes(record corestream.RecordValue) int64 {
	//: the two byte slices, nothing else.
	return int64(len(record.Key)) + int64(len(record.Payload))
}

// keyedPartition returns the partition a batch's keys name, and whether any
// record is keyed at all. Keyed records naming two partitions are refused:
// a batch is one partition's, written under one sync.
func keyedPartition(
	records []corestream.RecordValue, partitions int,
) (partition int, keyed bool, err error) {
	//: in order, so the refusal names the first record that disagrees.
	for i, record := range records {
		//: an unkeyed record follows the batch.
		if len(record.Key) == 0 {
			continue
		}
		placed := corestream.PartitionOf(record.Key, partitions)
		//: the first keyed record decides.
		if !keyed {
			partition, keyed = placed, true
			continue
		}
		//: every other keyed record must agree.
		if placed != partition {
			//: PartitionsMixed: nothing was written.
			return 0, false, partitionsMixed(i, partition, placed)
		}
	}
	//: the keys' partition, or none.
	return partition, keyed, nil
}

// carve appends src to arena and returns the arena and the copy, capped so an
// append to the copy cannot run into the next one. An empty src stays nil.
func carve(arena, src []byte) (grown, copied []byte) {
	//: nothing to copy keeps nil, as the caller's empty key reads.
	if len(src) == 0 {
		//: the arena unchanged.
		return arena, nil
	}
	start := len(arena)
	arena = append(arena, src...)
	//: the copy, its capacity cut at its end.
	return arena, arena[start:len(arena):len(arena)]
}

// copyEntries returns a copy of entries a reader may keep and modify: one
// slice of entries and one buffer for every byte they hold.
func copyEntries(entries []corestream.EntryValue) []corestream.EntryValue {
	//: an empty read is the nil slice.
	if len(entries) == 0 {
		//: nothing to copy.
		return nil
	}
	var size int64
	//: sized once, so the arena is one allocation.
	for i := range entries {
		size += recordBytes(entries[i].Record)
	}
	out := make([]corestream.EntryValue, len(entries))
	arena := make([]byte, 0, size)
	//: every entry, with bytes of its own.
	for i := range entries {
		out[i] = entries[i]
		arena, out[i].Record.Key = carve(arena, entries[i].Record.Key)
		arena, out[i].Record.Payload = carve(arena, entries[i].Record.Payload)
	}
	//: the reader's copy.
	return out
}
