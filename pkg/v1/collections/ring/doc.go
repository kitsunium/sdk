// Package ring is a bounded, lock-free queue for exactly ONE producer and
// exactly ONE consumer: a fixed number of slots, a write that refuses when they
// are full, and a read that refuses when they are empty — neither ever blocks.
//
//	q, err := ring.New[Record](1024)
//
//	// The producer's goroutine — and only that one — writes.
//	if err := q.TryWrite(rec); errors.Is(err, ring.Full) {
//	    dropped++ // the caller's policy: drop, retry, or block upstream
//	}
//
//	// The consumer's goroutine — and only that one — reads.
//	rec, err := q.TryRead()
//	if errors.Is(err, ring.Empty) {
//	    // nothing yet: wait on a signal, never spin
//	}
//
// It is the buffer behind the SDK's asynchronous logger, published as an alias
// of that kernel package (ADR 0159 §4).
//
// # When to use it
//
// Between one goroutine that produces on a hot path and one that consumes, when
// a full buffer must be an answer rather than a wait — a log record or a metric
// that the producer would rather drop than stall for. Two atomic loads and a
// store per operation, no lock and no allocation: an error is one of three
// sentinels, never built on the fly.
//
// For anything else a buffered channel is the right tool: it blocks, it wakes
// the reader, and it takes any number of producers and consumers.
//
// # One producer, one consumer — a precondition, not a hint
//
// EXACTLY ONE goroutine may call TryWrite at a time, and EXACTLY ONE may call
// TryRead. Concurrent producers, or concurrent consumers, corrupt the queue:
// serialise them upstream, or take the multi-producer sibling.
//
// # Many producers, one consumer
//
// [NewMulti] returns a [MultiQueue]: the same four methods, under the wider
// contract of any number of goroutines calling TryWrite at once and exactly
// one calling TryRead. Its capacity is rounded up to a power of two. A writer
// claims a slot, then stores into it: while it is between the two, the
// reader answers [Empty] for that slot and every one behind it. Publishing
// [Queue] froze its four methods, so this mode arrived as another
// constructor and a sibling interface (ADR 0039, ADR 0159 §2).
//
// # Errors
//
// [Full] answers a TryWrite on a full ring, [Empty] a TryRead on an empty one,
// and [CapZero] a [New] with a capacity that is not positive. Each is a fixed
// value, matched with errors.Is or the SDK's errs.HasCode.
package ring
