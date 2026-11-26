package cache

const (
	// queueMain is the LRU list, S3-FIFO's main queue and W-TinyLFU's
	// protected segment.
	queueMain queueID = iota
	// queueSmall is S3-FIFO's probationary queue and W-TinyLFU's window.
	queueSmall
	// queueProbation is W-TinyLFU's probationary segment.
	queueProbation
)

// queueID names the list an entry sits in.
type queueID uint8

// entry is one cache slot: it holds the key (so eviction can delete it from the
// map), the value, the prev/next links of the intrusive list it sits in (head =
// most recently inserted or promoted, tail = the next victim), and an optional
// deadline.
//
// The layout is a budget (#1038): for a string key and a word-sized value the
// node is 63 bytes, so it lands in the 64-byte size class — one class, and one
// cache line, since the runtime places an object of that class on a 64-byte
// boundary. A time.Time deadline (24 bytes) and a heap handle (8) had taken it
// to 80 bytes, two lines, and every insertion paid the larger class; the
// sketch's period (2) and hash (4) took the padding after the queue (#834).
type entry[K comparable, V any] struct {
	key  K
	val  V
	prev *entry[K, V]
	next *entry[K, V]
	// deadline is when the entry expires, in nanoseconds since the cache's
	// epoch (see UnlockedMap.deadlineAfter); meaningful only while slot is set.
	deadline int64
	// slot is the entry's position in the expiry heap plus one, and 0 while
	// it carries no deadline.
	slot uint32
	// weight is what the entry counts against MaxWeight: WeightOf's answer,
	// or 1 without one.
	weight int32
	// freq counts the hits since admission or the last demotion, capped at
	// maxFreq (S3-FIFO only).
	freq uint8
	// queue says which list the entry sits in (LRU uses main alone).
	queue queueID
	// period is the sketch's aging period in which the key last went
	// through the doorkeeper (W-TinyLFU only): a later use in that period
	// counts without asking the doorkeeper. A word: the marks wrap every
	// 65 535 periods, and the cache marks them all stale when they do.
	period uint16
	// hash is the key's hash under the frequency sketch's seed, taken once
	// when the entry is admitted (W-TinyLFU only): a hit counts the key
	// without hashing it again.
	hash uint32
}
