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
// consumers are NOT safe — those callers should serialise upstream or wrap
// this primitive with a mutex. The logger async sink takes the mutex-
// serialisation route (see internal/service/observe/logger/middleware/async); other
// domains pick whichever coordination fits their topology.
package ring
