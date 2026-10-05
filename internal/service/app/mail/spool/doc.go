// Package spool — the spool's construction parameters, the defaults their
// zero values clamp to, and the refusals.
//
// Package spool — one delivery: the record read back, a duplicate dropped,
// the attempt bounded, and its failure parked, or dead-lettered.
//
// Package spool — what a spool tells its observer, what an attempt knows
// about itself, and what a dead letter keeps.
//
// Package spool — a mail's identifier: the one rule it keeps, whoever minted
// it, because it becomes the left half of the mail's Message-ID.
//
// Package spool — the memory of what was delivered, which is what drops a
// redelivery instead of sending a mail twice.
//
// Package spool is outbound mail made durable: Send validates a mail and
// queues it, and the spool's consumer hands it to a mail transport, retrying a
// failure with a backoff and dead-lettering the mail after its last attempt. A
// redelivery of a mail it delivered is dropped, and every attempt, the one
// resend a crash can cause included, carries the same Message-ID. ADR 0111.
//
// # Where it sits
//
// It composes two domains and adds what neither has. The mail domain composes
// and refuses a mail and hands it to a transport, synchronously, once. The
// queue domain keeps bytes durably and delivers them at least once. The spool
// is the mail-shaped use of the queue: its records are mails, a failed
// delivery waits a backoff that grows with the attempt instead of the queue's
// fixed retry delay, the last failure is the dead letter's reason, and a
// redelivered mail that was in fact delivered is recognised and dropped.
//
// # What Send promises
//
// Send refuses, at the call site, everything the transport would refuse
// later: a header carrying a line break, an address that is not one, a mail
// with no recipient or no body. It stamps what a retry must not change — the
// sender when the mail names none, the Date, and a Message-ID made of the
// spool's identifier at the sender's domain — and returns once the mail is in
// the spool: durable, with a Dir, before Send returns.
//
// # An identifier the caller minted
//
// Send mints the mail's identifier with Config.NewID. SendWithID queues the
// mail under one its caller minted instead, for a caller that must know the
// identifier before the spool has the mail — a framework that holds a mail
// until a transaction commits returns the identifier at the call and queues
// the mail after the commit. Every identifier, whoever minted it, is
// non-empty, at most MaxIDBytes and an RFC 5322 dot-atom, because it becomes
// the left half of the mail's Message-ID: a caller's that is not is
// InvalidMailID, a generator's SpoolMisconfigured. A repeated identifier is
// not refused. The spool cannot see the identifiers in its queue without
// reading every record; it drops a mail under an identifier it delivered, as
// it drops every redelivery.
//
// # How a mail is delivered
//
// Run consumes the spool, one mail at a time, on the goroutine that calls it.
// Each attempt runs under SendTimeout, with its AttemptValue in the context.
// An attempt that fails with attempts left PARKS the mail: its lease is
// extended by Backoff.Delay(attempt) and left to lapse, and the queue hands it
// back then, its delivery count — the count that decides the dead letter, and
// that survives a crash — incremented. The last attempt's failure is the
// queue's nack, and the queue dead-letters the mail with that failure.
//
// # Exactly once, and the one duplicate left
//
// A mail this spool delivered is remembered by its identifier (the last
// DeliveredMemory of them), so a redelivery — the lease lapsed while the relay
// was still accepting the mail — is dropped rather than sent. The lease is
// twice SendTimeout, so that needs a relay slower than the timeout. What
// remains is a process that dies between the relay's acceptance and the
// acknowledgement: the next process sends the mail again, with the SAME
// Message-ID, which is how a receiver recognises it. No transport offers more:
// SMTP has no idempotent submission.
package spool
