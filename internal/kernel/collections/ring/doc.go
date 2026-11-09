// Package ring — range 0.1.3.* (ADR 0005 kernel/collections/ring block).
//
// Package ring — declares the sentinels returned by this package's
// constructor and TryWrite / TryRead operations. Each var's name equals its
// errs.Define Reason in SCREAMING_SNAKE form.
//
// Package ring provides a lock-free single-producer / single-consumer (SPSC)
// ring buffer built on sync/atomic. The SPSC constraint gives the maximum
// throughput for any hot-path producer that serialises enqueues — metrics
// batch writers, streaming codec pipelines, event-bus accumulators, or any
// other fixed-capacity non-blocking FIFO use case. Domain-neutral by design
// per the internal/kernel/ package rule (stdlib-only AND generic).
//
// SPSC means EXACTLY ONE goroutine calls TryWrite at a time AND EXACTLY ONE
// goroutine calls TryRead at a time. Concurrent producers or concurrent
// consumers are NOT safe on a Queue — those callers serialise upstream, or
// take the MPSC sibling: NewMulti returns a MultiQueue, the same four
// methods under the wider contract of any number of TryWrite callers and
// exactly one TryRead caller (Vyukov's bounded queue, a sequence number per
// cell, a power-of-two capacity).
//
// The SPSC ring keeps its consumer's cursor and its producer's each on a
// cache line of their own, with a cached copy of the other side's (read
// again only when the ring looks full or empty), and masks a cursor into a
// power-of-two storage instead of dividing (#1034).
package ring
