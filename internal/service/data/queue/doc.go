// Package queue — the consumer engine: the pull loop that runs a Handler on a
// CONSUMER'S goroutine, which is the third axis of ADR 0053's frontier.
//
// Package queue — the consumer engine's configuration, its one mandatory
// assertion, and the clamps that make every other field safe to omit.
//
// Package queue — who else can write a queue directory, asked of the mode bits:
// the whole answer on Unix, where a directory's other-write bit and its sticky
// bit are exactly the two facts the question needs.
//
// Package queue — who else can write a queue directory, asked of Windows in the
// only vocabulary it has for it: the directory's DACL, read by the one reader
// this repository has (internal/kernel/fs/winacl — the lock domain's reader,
// ADR 0084/0086, moved to the kernel so that the queue no longer reaches into
// another service for it; ADR 0159).
//
// # Why the mode rule cannot run here
//
// os.Stat SYNTHESISES a mode from FILE_ATTRIBUTE_READONLY on Windows, so every
// writable directory reports 0777 with no sticky bit, and the Unix rule refused
// every queue directory a caller could name — as QUEUE_DIRECTORY_UNUSABLE, an
// error that blames the deployment for the platform (ADR 0018 §(a)'s failure
// mode). The first Windows run of the whole suite found it: every /file case.
//
// # The same two questions, asked of the DACL
//
// The root is refused when an identifier meaning anybody may take away an
// entry it did not create — winacl.ReplaceRights, the Windows spelling of
// "world-writable and not sticky". A state is refused when such an identifier
// may put an entry there at all — a file (a planted message) or a directory
// or junction — or take one away, or alter a file created there through what
// that file would inherit: on Unix a message is written 0600 whatever the
// directory allows, here it inherits the directory's list.
//
// # Where it does not reach
//
// The broker cannot yet RUN here: internal/service/data/vfs refuses Windows by
// design (no flushable directory handle, a mode that is not an ACL), so NewFile
// returns UNSUPPORTED_PLATFORM once these checks pass. They run anyway, so the
// refusal a caller meets on Windows is the platform's rather than a false
// verdict on their directory — and so the rules are right on the day vfs gains
// a Windows backend.
//
// Package queue — the durable broker: one directory, three subdirectories,
// and rename(2) as the only thing that changes a fact.
//
// Package queue — the durable broker's configuration, and the directory
// checks it runs before anything is opened.
//
// Package queue — the durable broker's failure path: handing a message back,
// renewing a lease, and the record left behind for whoever investigates.
//
// Package queue — the durable queue's state machine is a NAME. This file is
// its grammar: what a queued, an in-flight and an abandoned message are
// called, and how a name is read back into the facts it carries.
//
// Package queue — the durable broker's read path: reclaiming the leases of
// consumers that died, and leasing what is visible.
//
// Package queue — the durable broker's dead-letter decisions: putting a dead
// letter back into the queue, or deleting it.
//
// Package queue — one entry in the in-memory broker's lease-deadline heap.
//
// Package queue — the record the in-memory broker keeps about one message.
//
// Package queue — the in-memory broker: the double a consumer's own tests run
// against, and the control the durability measurements are read against.
//
// Package queue — the in-memory broker's configuration.
//
// Package queue implements the asynchronous, durable message queue declared
// in internal/core/data/queue (ADR 0054): three brokers — one in the heap, one on
// the filesystem, one in a table of the caller's own SQL database (ADR 0151) —
// the consumer engine that drives a [corequeue.Handler] against any of them,
// and the dead-letter record they all write.
//
// # Three brokers, one set of refusals
//
// [NewMemory] exists so a consumer can test its own code without a directory
// or a database, and that is only worth something if the brokers answer
// identically. They therefore share the core policy guard
// (corequeue.PolicyValue.Validate), the retry delay (retryDelay), the same
// typed sentinels, and a table-driven conformance suite that runs the SAME
// cases against all three. Where they cannot be identical — surviving a
// restart, crossing a process boundary, joining the caller's transaction, what
// a flush costs — the difference is named rather than discovered by a reader.
//
// # The one thing the SQL broker does that neither other can
//
// [NewSQL] keeps the queue in one table of the database the application
// already writes, and every call runs on the transaction its context carries.
// So a message published inside the caller's transaction exists if and only if
// that transaction commits: the transactional outbox, which no broker whose
// publication is durable on its own can offer.
//
// # The one thing the file broker does that the memory broker cannot
//
// It survives the consumer. Every piece of state the durable queue keeps —
// which messages are queued, which are leased, when each lease lapses, how
// many times each has been delivered — lives in a DIRECTORY ENTRY, not in
// this process's heap. A consumer that is SIGKILLed mid-handler leaves an
// entry whose deadline is in the past, and the next consumer to look, in any
// process on that machine, renames it back and delivers it again.
// TestAKilledConsumerLosesItsLeaseAndTheMessageComesBack does exactly that,
// with a real subprocess and a real SIGKILL, because a test that calls Nack
// proves only that Nack works.
//
// # Why the durable broker holds no long-lived handle
//
// There is no Close in this package and no descriptor kept between calls.
// Every operation opens what it needs, moves what it must, and returns. That
// costs a syscall or two per call and buys the property the domain is for: a
// process that dies leaves nothing half-open, nothing to recover, and no
// state that only it could have interpreted. It is also what makes the
// broker genuinely inter-process — two brokers in two processes over one
// directory are the same queue, with no coordination beyond the filesystem.
//
// # Why there is no lock file
//
// Exclusion between consumers is [os.Rename], not a lock. Renaming a queued
// message into the in-flight directory is atomic and it FAILS for the loser,
// so two consumers reaching for the same message resolve it in the kernel
// with no lock, no lease and no polling — and it resolves the same way for
// two goroutines in one process as for two processes, which ADR 0052
// measured that flock(2) emphatically does not.
//
// Package queue — how long a nacked message waits, the same way in all three
// brokers.
//
// Package queue — the SQL broker: the queue in one table of the caller's own
// database, every call on the transaction its context carries (ADR 0151).
//
// Package queue — the SQL broker's construction parameters, and the refusals
// a configuration no SQL broker could run gets.
//
// Package queue — the SQL broker's dead letters: burying one a handler
// rejected, reading them back, and the two decisions an operator takes.
//
// Package queue — the only place the SQL broker renders SQL. Every statement
// it sends is built here, once, at NewSQL, for its dialect and its table; the
// two whose length depends on a batch are rendered per call from the same
// parts. Every one is spelled with the vocabulary core/data/sql's Dialect owns: the
// bind markers, the quoting and the row lock.
//
// Package queue — the SQL broker's table, as a migration the caller runs under
// its own version table.
//
// Package queue — the SQL broker's read path: leasing what is due, and
// burying the leases whose consumers died with no attempt left.
//
// Package queue — waking an idle consumer: the broadcast every broker closes on
// a publication, and the table that lets two durable brokers over one queue —
// one directory, or one database and table — in one process wake each other's
// consumers.
package queue
