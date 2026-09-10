// Package mail — the transport port and its ADR 0039 capability siblings.
//
// They live apart from the value types because a port and a value change for
// different reasons: the port is FROZEN and the values are not.
package mail

import "context"

// Transport carries a message to whatever will deliver it. It is FROZEN at one
// method.
//
// One method, because "send this mail" is the only thing every transport can
// do. A capability that some transports have arrives as a SIBLING interface
// reached by type assertion, never as a second method (ADR 0039): pkg/v1/mail
// aliases this type, Go interfaces are structural, and a second method would
// break every downstream implementation at compile time with no deprecation
// window.
//
// Implementations MUST be safe for concurrent use.
//
// What a Transport does NOT promise is as important as what it does. A nil
// error means the message was ACCEPTED by the next hop — nothing more. It is
// not a promise of delivery, of inbox placement, or of the recipient existing:
// SMTP accepts responsibility hop by hop (RFC 5321 §6.1), and the hop that
// eventually refuses reports it in a bounce message hours later, to the
// envelope's return path, over a channel this port has no access to.
//
// It is named for the ROLE it plays rather than for its one method: "the SMTP
// transport" and "the in-memory transport" are the words this domain is
// discussed in, and a Sender would name the verb instead.
//
// IFACE-PLUGIN: the concrete transports stay unexported behind their
// constructors in internal/service/mail.
type Transport interface {
	// Send composes msg and hands it to the next hop.
	//
	// It validates before it dials: a message that cannot be composed is
	// refused without opening a socket, so a header-injection attempt never
	// reaches a server and never costs a connection.
	//
	// ctx bounds the whole attempt. A transport that cannot honour a deadline
	// natively must still stop when ctx does — by severing its own connection
	// if that is the only mechanism available — because a Send that outlives
	// its context is a goroutine the caller believes it has cancelled.
	Send(ctx context.Context, msg MessageValue) error
}

// BatchSender sends several messages over ONE session. It is the ADR 0039
// sibling of [Transport], reached by type assertion:
//
//	if batch, ok := transport.(mail.BatchSender); ok {
//		err = batch.SendBatch(ctx, notifications)
//	}
//
// It is a capability and not a method, because it is not one for every
// transport: an HTTP provider API has no session to reuse, and a transport
// that implemented it by looping would be advertising an optimisation it does
// not perform.
//
// The semantics of a partial failure are the reason this is not just a loop in
// the caller: SendBatch sends every message it can and returns the failures
// AGGREGATED with errors.Join, rather than stopping at the first. One bad
// recipient in a batch of five hundred must not silence the other four hundred
// and ninety-nine, and the caller still learns about it — errs.HasCode walks
// Unwrap() []error, so a joined error answers the same questions a single one
// does.
type BatchSender interface {
	// SendBatch sends every message in msgs and returns the joined failures.
	// A nil error means every message was accepted.
	SendBatch(ctx context.Context, msgs []MessageValue) error
}

// Outbox exposes what a transport has been asked to send. It is the ADR 0039
// sibling that makes a test double a double rather than a stub:
//
//	if box, ok := transport.(mail.Outbox); ok {
//		sent := box.Sent()
//	}
//
// Only an in-memory transport implements it, and that is the point — a
// consumer's test asserts against this interface without importing the SDK's
// service layer, and a production transport that started implementing it would
// be a transport keeping every message it ever sent in memory.
type Outbox interface {
	// Sent returns the deliveries recorded so far, oldest first. The returned
	// slice and every byte slice in it are the caller's own copy.
	Sent() []DeliveryValue
	// Reset discards the record, so one test's messages are not another's.
	Reset()
}

// FullTransport is the union the SDK's in-memory transport returns: a
// [Transport] that also batches and records.
//
// It exists for the same reason core/vfs.FullFS and core/metrics.FullMeter do —
// the port stays frozen and the capabilities stay assertable for a THIRD-PARTY
// transport that has neither, while a caller wiring one of the SDK's own does
// not have to type-assert for a feature it just constructed. A parameter should
// still ask for the narrowest thing it uses.
type FullTransport interface {
	Transport
	BatchSender
	Outbox
}
