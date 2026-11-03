package cache

import (
	"time"

	"github.com/kitsunium/sdk/internal/kernel/collections/heap"
)

const (
	// queueMain is the LRU list, and S3-FIFO's main queue.
	queueMain queueID = iota
	// queueSmall is S3-FIFO's probationary queue.
	queueSmall
)

// queueID names the list an entry sits in.
type queueID uint8

// entry is one cache slot: it holds the key (so eviction can delete it from the
// map), the value, an optional absolute expiry, and the prev/next links of the
// intrusive list it sits in (head = most recently inserted or promoted, tail =
// the next victim).
type entry[K comparable, V any] struct {
	key      K
	val      V
	expireAt time.Time // zero = no expiry
	prev     *entry[K, V]
	next     *entry[K, V]
	// expiry names the entry's node in the expiry heap; the zero Handle when
	// it carries no deadline.
	expiry heap.Handle
	// weight is what the entry counts against MaxWeight: WeightOf's answer,
	// or 1 without one. 32 bits, so that with freq and queue it fills one word
	// and the node stays in the 80-byte size class for a string key and a
	// word-sized value.
	weight int32
	// freq counts the hits since admission or the last demotion, capped at
	// maxFreq (S3-FIFO only).
	freq uint8
	// queue says which list the entry sits in (S3-FIFO only; LRU uses main).
	queue queueID
}
