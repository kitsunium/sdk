//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/mail/spool .

// Package spool is the public facade for the durable mail outbox: the place a
// mail waits between the call that wants it sent and the relay that takes it.
//
// A mail transport (package [github.com/kitsunium/sdk/pkg/v1/app/mail]) sends
// once, synchronously, and a relay that is down makes that the caller's
// problem. A [Spool] is the outbox between them: [Spool].Send validates a
// mail — refusing at the call site everything the transport would refuse later
// — stamps what a retry must not change (the sender from [Config].From when
// the mail names none, the Date, and a Message-ID made of the spool's
// identifier at the sender's domain), and returns once the mail is queued:
// durable, in a directory that outlives the process, when [Config].Dir is set.
// [Spool].Run hands each mail to the transport, one at a time, and a failure
// waits a backoff that grows with the attempt — one second, doubling, to five
// minutes by default — before the next; after [Config].MaxAttempts the mail is
// dead-lettered with its last failure, readable through [Spool].DeadLetters.
//
//	outbox, err := spool.New(spool.Config{
//		Transport:   transport, // a mail.Transport: mail.NewSMTP, mail.NewCapture, …
//		Dir:         "/var/lib/app/outbox", // empty: in memory
//		MaxAttempts: 6,
//		From:        mail.Address{Name: "App", Addr: "app@example.com"},
//	})
//	go outbox.Run(ctx) // or under lifecycle.NewSupervisor
//	id, err := outbox.Send(ctx, mail.Message{To: to, Subject: "Welcome", Text: body})
//
// A redelivery of a mail the spool delivered — its lease lapsed while a slow
// relay was still accepting it — is recognised by its identifier and dropped.
// The one duplicate no outbox can prevent is a process that dies between the
// relay's acceptance and the acknowledgement; the next process sends the mail
// again under the SAME Message-ID, which is how a receiver recognises it.
// Every attempt carries its [Attempt] in its context — the identifier, the
// count, and what [Config].Annotate kept from the Send's context — so a
// transport can continue the Send's trace, and every mail's fate reaches
// [Config].Observe. The spool writes nothing anywhere itself.
//
// # An identifier minted before the mail is spooled
//
// [Spool].Send mints the mail's identifier. A caller that must know it before
// the spool has the mail — a framework that holds a mail until a transaction
// commits, and returns the identifier at the call — mints it itself and
// queues the mail later with [Spool].SendWithID, which stamps what Send
// stamps and makes the Message-ID of that identifier:
//
//	mailIDs, err := id.NewTypeID("mail") // github.com/kitsunium/sdk/pkg/v1/app/id
//	outboxID, err := mailIDs.New()       // at the call, inside the transaction
//	// … once the transaction has committed:
//	err = outbox.SendWithID(ctx, outboxID, msg)
//
// The identifier becomes the left half of the mail's Message-ID, so it must be
// an RFC 5322 dot-atom — printable ASCII, no space, no special, no empty label
// — of at most [MaxIDBytes] bytes, even for a mail that brings its own
// Message-ID. Anything else is [InvalidMailID], which never quotes the
// identifier, and nothing is queued. A [Config].NewID that mints such an
// identifier is [Misconfigured] at Send.
//
// A repeated identifier is not refused. A mail under one the spool delivered
// is dropped at delivery as an [EventDuplicate], after its own [EventQueued],
// so a SendWithID retried after an ambiguous failure sends the mail once while
// the process remembers it. A mail dead-lettered under an identifier was never
// delivered, and can be queued again under it. After a restart the spool
// remembers nothing, and a repeat is sent again — under the same Message-ID,
// when that was made of the identifier.
//
// # Why a package of its own
//
// The outbox stands on the queue domain, and a durable one on the filesystem
// and the SQL ports beneath it. A program that only composes and sends mail
// imports package mail and links none of that; one that wants the outbox
// imports this package as well.
package spool

import (
	"context"
	"time"

	corespool "github.com/kitsunium/sdk/internal/core/app/mail/spool"
	svcspool "github.com/kitsunium/sdk/internal/service/app/mail/spool"
)

// The spool's defaults, and its bound on an identifier.
const (
	// DefaultSendTimeout bounds one delivery attempt when Config.SendTimeout
	// is not positive.
	DefaultSendTimeout time.Duration = svcspool.DefaultSendTimeout
	// DefaultRetryBase and DefaultRetryMax bound the wait between attempts
	// when Config.Backoff is zero; DefaultRetryBase is also the first wait of
	// a curve that sets no BaseDelay.
	DefaultRetryBase time.Duration = svcspool.DefaultRetryBase
	DefaultRetryMax  time.Duration = svcspool.DefaultRetryMax
	// DefaultMaxMessageBytes bounds one spooled mail when
	// Config.MaxMessageBytes is zero.
	DefaultMaxMessageBytes int = svcspool.DefaultMaxMessageBytes
	// DeliveredMemory is how many delivered mails a spool remembers to drop a
	// redelivery.
	DeliveredMemory int = svcspool.DeliveredMemory
	// MaxIDBytes bounds a spooled mail's identifier, whoever minted it:
	// Spool.SendWithID refuses a longer one with InvalidMailID.
	MaxIDBytes int = svcspool.MaxIDBytes
)

// What can happen to a spooled mail.
const (
	// EventQueued: Send put the mail in the spool.
	EventQueued EventKind = svcspool.EventQueued
	// EventSent: the transport accepted the mail.
	EventSent EventKind = svcspool.EventSent
	// EventRetrying: an attempt failed and the next is due at Next.
	EventRetrying EventKind = svcspool.EventRetrying
	// EventDeadLettered: the last attempt failed; the mail is kept with it.
	EventDeadLettered EventKind = svcspool.EventDeadLettered
	// EventDuplicate: a redelivery of a mail already delivered was dropped.
	EventDuplicate EventKind = svcspool.EventDuplicate
)

// Spool is the public alias for the durable outbox: Send, SendWithID, Run,
// DeadLetters, Close.
type Spool = svcspool.Spool

// Config is the public alias for a spool's configuration: Transport and
// MaxAttempts required; Dir, Clock, From, Backoff, SendTimeout,
// MaxMessageBytes, PollInterval, Observe, Annotate and NewID optional.
type Config = svcspool.Config

// Event is the public alias for one thing that happened to one mail.
type Event = svcspool.EventValue

// EventKind is the public alias for what an Event reports.
type EventKind = svcspool.EventKind

// Attempt is the public alias for what one delivery attempt knows about
// itself, carried in the context the spool hands its transport.
type Attempt = svcspool.AttemptValue

// DeadLetter is the public alias for a mail the spool gave up on.
type DeadLetter = svcspool.DeadLetterValue

// The spool's sentinels (0.3.81.*). errors.Is matches one, and errs.CodeOf
// reads its code.
var (
	// Misconfigured refuses a spool that could never deliver, and a Send
	// whose Config.NewID minted an identifier no mail can keep.
	Misconfigured = corespool.SpoolMisconfigured
	// Closed refuses a Send or a SendWithID after Close.
	Closed = corespool.SpoolClosed
	// MessageUndecodable reports a spooled record that is not a mail.
	MessageUndecodable = corespool.MessageUndecodable
	// MessageUnencodable refuses a mail Send could not write.
	MessageUnencodable = corespool.MessageUnencodable
	// TransportPanicked is the failure of an attempt whose transport
	// panicked.
	TransportPanicked = corespool.TransportPanicked
	// InvalidMailID refuses an identifier Spool.SendWithID was given that is
	// empty, longer than MaxIDBytes or not an RFC 5322 dot-atom. It names the
	// rule broken and never the identifier.
	InvalidMailID = corespool.InvalidMailID
)

// New builds a durable outbox over cfg.Transport: a queue in cfg.Dir, or in
// memory without one. It starts nothing: Run delivers.
func New(cfg Config) (*Spool, error) {
	//: delegate to the service constructor.
	return svcspool.New(cfg)
}

// AttemptFrom returns the attempt a delivery context carries — only a context
// a Spool handed its transport carries one.
func AttemptFrom(ctx context.Context) (attempt Attempt, ok bool) {
	//: delegate to the service accessor.
	return svcspool.AttemptFrom(ctx)
}
