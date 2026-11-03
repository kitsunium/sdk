// Package queue is a core/data/queue Broker over a Valkey or Redis stream and
// one consumer group (ADR 0054's deferred connector, ADR 0211), in the
// go-redis vendor module named by its vendor (#984).
//
// # Mapping
//
// Publish is XADD, the entry id the message's ID. Receive first reclaims what
// lapsed — a lease past its deadline, dead-lettered when its deliveries are
// spent —, leases again the hand-backs whose instant has come (XCLAIM), then
// reads new entries with XREADGROUP; the pending-entries list holds every
// message from its first lease to its XACK. Ack is XACK and XDEL, so one
// stream is one queue. A message that runs out of attempts moves to the
// dead-letter stream, its cause beside it, in the same script that removes
// it. Receive and every settlement are one Lua script, atomic on the
// server, and every key of one broker shares the stream's hash tag, so a
// broker lives on one slot of a cluster. A failed command is
// StreamUnreachable or CommandRefused, core/data/queue.QueueBackendFailed
// in its trail.
//
// # Whose clock is the lease
//
// The lease deadlines are the broker's, read from StreamConfig.Clock and kept
// in a sorted set beside the stream, to the microsecond — not the server's idle
// time, which XAUTOCLAIM would read: a test drives every deadline with a
// ManualClock, as for the SQL broker, and replicas agree as far as their
// clocks do. The broker computes every instant and passes it to the script
// as text. A receipt names the entry, the token of its lease and its
// delivery, so a lapsed or replaced lease is LeaseExpired and one this
// package never minted UnknownReceipt.
//
// # Capabilities
//
// Waker (a publication or hand-back by this process, and the next deadline
// it knows of), LeaseExtender, Rejecter, Releaser, DelayedNacker and
// DeadLetterReader. Out of scope: hash-slot placement across a cluster (one
// stream is one slot), and a partitioned log, which is data/stream's.
package queue
