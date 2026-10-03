//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/collections/ring .

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
// serialise them upstream, as the SDK's async logger does with a mutex. A
// multi-producer mode is planned beside this one (ADR 0159 §2); publishing
// [Queue] froze its four methods, so that mode arrives as another constructor
// and never as a change to them.
//
// # Errors
//
// [Full] answers a TryWrite on a full ring, [Empty] a TryRead on an empty one,
// and [CapZero] a [New] with a capacity that is not positive. Each is a fixed
// value, matched with errors.Is or the SDK's errs.HasCode.
package ring

import (
	kring "github.com/kitsunium/sdk/internal/kernel/collections/ring"
)

// Queue is the single-producer, single-consumer ring.
//
// TryWrite enqueues an item without blocking, or returns [Full]. TryRead
// dequeues the oldest item without blocking, or returns [Empty]; the slot it
// read is cleared, so the queue keeps no reference to an item it handed out.
// Capacity is the number of items the ring holds when full, fixed at [New].
// Len is a snapshot count — the producer or the consumer may move it before
// the caller acts on it.
//
// Its four methods are frozen: a published interface grows by a sibling,
// never by a fifth method (ADR 0039).
type Queue[T any] = kring.Queue[T]

// New returns an empty [Queue] holding at most capacity items, or [CapZero]
// when capacity is not positive. The storage is allocated once, here.
func New[T any](capacity int) (Queue[T], error) {
	//: the kernel owns the ring; this facade only forwards.
	return kring.New[T](capacity)
}

// The sentinels a caller matches with errors.Is or errs.HasCode.
var (
	// Full is returned by TryWrite when every slot is taken; the caller
	// decides whether to drop, retry or slow its producer.
	Full = kring.Full

	// Empty is returned by TryRead when no item is waiting; the caller
	// decides whether to wait on a signal or yield — never to spin.
	Empty = kring.Empty

	// CapZero is returned by New for a capacity that is not positive: a ring
	// that can hold nothing is a configuration mistake, not a queue.
	CapZero = kring.CapZero
)
