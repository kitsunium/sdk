package stream

// The 32-bit FNV-1a parameters (hash/fnv's), inlined so that placing a key
// costs no allocation and no hash.Hash32 interface value.
const (
	fnvOffset32 uint32 = 2166136261
	fnvPrime32  uint32 = 16777619
)

// PartitionOf returns the partition a record keyed key is appended to among
// partitions: the 32-bit FNV-1a hash of the key modulo partitions. It is the
// one placement every engine applies, so a key lands on the same partition in
// memory, on a directory and in a table.
//
// A partitions below one is read as one. An empty key has no partition of its
// own: an engine spreads unkeyed batches round-robin and does not ask; asked
// anyway, PartitionOf answers the hash of no bytes.
func PartitionOf(key []byte, partitions int) int {
	//: one partition, or a count that is not one, places every key on the first.
	if partitions <= 1 {
		return 0
	}
	hash := fnvOffset32
	//: FNV-1a: xor the byte in, then multiply.
	for _, b := range key {
		hash ^= uint32(b)
		hash *= fnvPrime32
	}
	//: the modulo in uint64, so a partition count above 2^31 on a 64-bit
	//: platform cannot overflow and the result fits an int everywhere.
	return int(uint64(hash) % uint64(partitions))
}
