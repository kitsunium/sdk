package cache

import "math/bits"

const (
	// sketchSample is the sketch's aging period, in increments per entry of
	// the bound: every sketchSample × bound increments, every counter is
	// halved, so the counts follow what is used now.
	sketchSample int = 10
	// blockWords is a block's words: eight, one cache line.
	blockWords int = 8
	// sketchMaxEntries caps the keys a table is sized for at 2^27 — a
	// gibibyte of counters, past any bound the entries' memory allows —, so
	// the period and the doorkeeper's bits fit an int on 32 bits too.
	sketchMaxEntries int = 1 << 27
	// counterMax is a four-bit counter's ceiling.
	counterMax uint64 = 15
	// counterLowBits masks the low bit of every four-bit counter of a word.
	counterLowBits uint64 = 0x1111111111111111
	// counterHalfMask clears what a one-bit shift carries into each counter's
	// top bit from its neighbour.
	counterHalfMask uint64 = 0x7777777777777777
	// counterMix remixes a hash into the bytes that pick a key's counters.
	counterMix uint32 = 0x31848bab
	// doorBitsPerSample sizes the doorkeeper: four bits per increment of
	// the aging period, so the keys used once in a period fill it sparsely.
	doorBitsPerSample int = 4
	// doorMinBits is the smallest doorkeeper: one word.
	doorMinBits int = 64
	// doorMaxBits caps the doorkeeper at 2^30 bits (128 MiB).
	doorMaxBits int = 1 << 30
	// doorMixLow and doorMixHigh spread a hash into the doorkeeper's three
	// bit positions.
	doorMixLow uint64 = 0x9e3779b97f4a7c15
	// doorMixHigh is the second multiplier, for the third position.
	doorMixHigh uint64 = 0xc2b2ae3d27d4eb4f
)

// sketchBlock is eight words of sixteen four-bit counters, one cache line: a
// key's four counters all lie in one block.
type sketchBlock [blockWords]uint64

// frequencySketch counts how often each key was used, approximately and in
// bounded memory: a count-min sketch of four-bit counters, sixteen to a word,
// a key's four counters in one block of eight words — one cache line, so a
// count costs one miss at most (Caffeine's layout) — behind TinyLFU's
// doorkeeper, a Bloom filter that takes a key's first use in an aging period
// so that a key used once never reaches the counters (Einziger, Friedman and
// Manes, "TinyLFU", ACM TOS 2017). A held key's estimate is the least of its
// four counters, plus one when its entry went through the doorkeeper in the
// current aging period. Caller holds the cache's lock.
type frequencySketch struct {
	// blocks holds the counters; nil for a cache that never evicts.
	blocks []sketchBlock
	// door is the doorkeeper's bits, cleared at every aging.
	door []uint64
	// blockMask picks a block from a hash.
	blockMask uint32
	// doorMask picks a doorkeeper bit from a mixed hash.
	doorMask uint32
	// additions counts the increments since the last aging.
	additions int
	// sampleSize is the aging period.
	sampleSize int
	// period numbers the aging periods, wrapping: an entry records the one
	// in which its key last went through the doorkeeper.
	period uint8
}

// size gives the sketch a table for entries keys: the power of two of words
// at or above it, one word — sixteen counters — a key, an aging period of
// sketchSample × entries increments, and a doorkeeper of four bits an
// increment of the period. The counts start over.
func (s *frequencySketch) size(entries int) {
	entries = min(max(entries, 1), sketchMaxEntries)
	blocks := 1
	//: the power of two of blocks whose words reach the entries.
	for blocks*blockWords < entries {
		blocks <<= 1
	}
	table := make([]sketchBlock, blocks)
	s.blocks = table
	s.blockMask = uint32(blocks) - 1
	s.additions = 0
	s.sampleSize = sketchSample * entries
	bits := doorMinBits
	//: the power of two of bits at or above four a sample, within the cap.
	for bits/doorBitsPerSample < s.sampleSize && bits < doorMaxBits {
		bits <<= 1
	}
	door := make([]uint64, bits/doorMinBits)
	s.door = door
	s.doorMask = uint32(bits - 1)
}

// fit grows the table to entries keys when its words are fewer — a cache
// bounded by weight alone sizes its sketch by its live count. The counts
// start over on growth.
func (s *frequencySketch) fit(entries int) {
	words := len(s.blocks) * blockWords
	//: room enough already.
	if entries <= words || words >= sketchMaxEntries {
		//: kept.
		return
	}
	s.size(entries)
}

// clear forgets every count and the doorkeeper, keeping the tables.
func (s *frequencySketch) clear() {
	clear(s.blocks)
	clear(s.door)
	s.additions = 0
}

// record counts the use of a key the cache does not hold — a key being
// admitted —, and records in *seen the period it went through: its first use
// since the last aging sets its doorkeeper bits and leaves the counters
// alone, a later one — a key evicted and back within the period — counts.
// Nothing without a table.
func (s *frequencySketch) record(hash uint32, seen *uint8) {
	//: a cache that never evicts keeps no counts.
	if len(s.blocks) == 0 {
		//: nothing to count.
		return
	}
	first, second, third := s.doorCells(hash)
	*seen = s.period
	//: seen in this period: the sketch counts it.
	if s.doorHolds(first, second, third) {
		s.increment(hash)
		//: counted.
		return
	}
	s.door[first>>6] |= 1 << (first & 63)
	s.door[second>>6] |= 1 << (second & 63)
	s.door[third>>6] |= 1 << (third & 63)
}

// touch counts the use of a key the cache holds, whose entry records in
// *seen the period it last went through the doorkeeper: a later use in that
// period counts in the sketch, the first of a new one goes through the
// doorkeeper — the doorkeeper's answer for a resident key, without reading
// its bits.
func (s *frequencySketch) touch(hash uint32, seen *uint8) {
	//: let through in this period already: counted.
	if *seen == s.period {
		s.increment(hash)
		//: counted.
		return
	}
	s.record(hash, seen)
}

// estimateHeld returns how often a key the cache holds was used: its
// counters' least, plus one when its entry went through the doorkeeper in
// this period — exact where the doorkeeper's bits could be shared.
func (s *frequencySketch) estimateHeld(hash uint32, seen uint8) uint64 {
	count := s.frequency(hash)
	//: the use the doorkeeper took in this period.
	if len(s.blocks) != 0 && seen == s.period {
		count++
	}
	//: the estimate.
	return count
}

// doorCells returns the doorkeeper's three bit positions for hash.
func (s *frequencySketch) doorCells(hash uint32) (first, second, third uint32) {
	low := uint64(hash) * doorMixLow
	high := uint64(hash) * doorMixHigh
	//: three positions out of two products.
	return uint32(low) & s.doorMask, uint32(low>>32) & s.doorMask, uint32(high>>32) & s.doorMask
}

// doorHolds reports whether the doorkeeper holds the three positions.
func (s *frequencySketch) doorHolds(first, second, third uint32) bool {
	//: every bit set.
	return s.door[first>>6]&(1<<(first&63)) != 0 &&
		s.door[second>>6]&(1<<(second&63)) != 0 &&
		s.door[third>>6]&(1<<(third&63)) != 0
}

// counters returns hash's block and its counter hash: the block from the
// hash's low bits, each of the four counters from a byte of its remix — a
// word of its pair from the byte's low bit, a nibble of the word from the
// four above.
func (s *frequencySketch) counters(hash uint32) (block *sketchBlock, mix uint32) {
	mix = hash * counterMix
	mix ^= mix >> 14
	//: one bounds check: the mask is the table's length less one.
	return &s.blocks[hash&s.blockMask], mix
}

// increment counts one more use of the key of hash, ages the table every
// sampleSize increments, and does nothing without a table. A counter at its
// ceiling stays there; the four updates are branch-free.
func (s *frequencySketch) increment(hash uint32) {
	//: a cache that never evicts keeps no counts.
	if len(s.blocks) == 0 {
		//: nothing to count.
		return
	}
	block, mix := s.counters(hash)
	added := bump(&block[mix&1], mix>>1&15) |
		bump(&block[2|mix>>8&1], mix>>9&15) |
		bump(&block[4|mix>>16&1], mix>>17&15) |
		bump(&block[6|mix>>24&1], mix>>25&15)
	//: every counter of the key at its ceiling: nothing was added.
	if added == 0 {
		//: no increment to age by.
		return
	}
	s.additions++
	//: the period is over: halve every count.
	if s.additions >= s.sampleSize {
		s.age()
	}
}

// bump adds one to the counter at nibble of word unless it is at its
// ceiling, and returns 1 when it did, 0 otherwise — without a branch: a
// counter of 15 plus one carries into bit 4.
func bump(word *uint64, nibble uint32) uint64 {
	shift := nibble << 2
	room := (*word>>shift&counterMax+1)>>4 ^ 1
	*word += room << shift
	//: 1 when counted.
	return room
}

// frequency returns the least of the four counters of the key of hash, 0 to
// 15, and 0 without a table.
func (s *frequencySketch) frequency(hash uint32) uint64 {
	//: a cache that never evicts keeps no counts.
	if len(s.blocks) == 0 {
		//: never counted.
		return 0
	}
	block, mix := s.counters(hash)
	least := block[mix&1] >> ((mix >> 1 & 15) << 2) & counterMax
	least = min(least, block[2|mix>>8&1]>>((mix>>9&15)<<2)&counterMax)
	least = min(least, block[4|mix>>16&1]>>((mix>>17&15)<<2)&counterMax)
	//: the least of the four.
	return min(least, block[6|mix>>24&1]>>((mix>>25&15)<<2)&counterMax)
}

// age halves every counter and the count of additions — less the counters'
// lost halves, so the period stays sampleSize increments — and empties the
// doorkeeper.
func (s *frequencySketch) age() {
	odd := 0
	//: one shift a word halves its sixteen counters at once.
	for at := range s.blocks {
		block := &s.blocks[at]
		//: the block's eight words.
		for w, word := range block {
			odd += bits.OnesCount64(word & counterLowBits)
			block[w] = word >> 1 & counterHalfMask
		}
	}
	s.additions = (s.additions - odd/4) / 2
	clear(s.door)
	s.period++
}
