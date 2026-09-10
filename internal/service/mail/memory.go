// Package mail — the in-memory transport, which is a DOUBLE and not a stub.
package mail

import (
	"context"
	"errors"
	"slices"
	"sync"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
)

// memoryTransport records what it was asked to send instead of sending it.
type memoryTransport struct {
	composer *Composer
	mutex    sync.RWMutex
	sent     []coremail.DeliveryValue
}

// NewMemory returns a transport that composes every message and keeps the
// result instead of dialling anything.
//
// It COMPOSES, and that is the whole design. A stub that recorded the Message
// value would accept a subject carrying a CRLF, an inline attachment with no
// body, a Bcc the caller expected in a header — every refusal the SMTP
// transport makes, made nowhere — and a consumer's test suite would pass right
// up until production. Because this transport runs the same composer, a message
// refused in production is refused in the test that was supposed to catch it,
// with the same typed error.
//
// It takes no arguments for the same reason core/vfs.NewMem does: every knob it
// could offer is one a consumer's test must set before it can assert anything,
// and the value of a double is that it costs one line.
//
// Safe for concurrent use.
func NewMemory() coremail.FullTransport {
	//: the same composer the SMTP transport builds, with the same defaults.
	return &memoryTransport{composer: NewComposer(ComposerConfig{})}
}

// Send composes msg and records the delivery. ctx is honoured — a cancelled
// context refuses before anything is recorded — so a test exercising
// cancellation behaves the way the real transport does.
func (t *memoryTransport) Send(ctx context.Context, msg coremail.MessageValue) error {
	//: the same cancellation contract as a transport that dials.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: DialFailed: nothing was sent, and the reason is the caller's.
		return wrapAs(DialFailed, ctxErr)
	}
	envelope, envelopeErr := msg.Envelope()
	//: the core verdict, which also runs Validate.
	if envelopeErr != nil {
		//: nothing recorded.
		return envelopeErr
	}
	raw, composeErr := t.composer.Compose(msg)
	//: HeaderInjection, HeaderTooLong or ComposeFailed.
	if composeErr != nil {
		//: nothing recorded.
		return composeErr
	}
	t.mutex.Lock()
	defer t.mutex.Unlock()
	//: the bytes are this transport's own copy from here on.
	t.sent = append(t.sent, coremail.DeliveryValue{Envelope: envelope, Raw: raw})
	//: accepted.
	return nil
}

// SendBatch sends every message and joins the failures, so one bad recipient
// in five hundred does not silence the other four hundred and ninety-nine.
func (t *memoryTransport) SendBatch(ctx context.Context, msgs []coremail.MessageValue) error {
	//: exactly as many slots as there are messages, at most.
	failures := make([]error, 0, len(msgs))
	//: every message is attempted, including the ones after a failure.
	for _, msg := range msgs {
		//: one verdict per message.
		if sendErr := t.Send(ctx, msg); sendErr != nil {
			failures = append(failures, sendErr)
		}
	}
	//: errors.Join returns nil for an empty slice, and errs.HasCode walks
	//: Unwrap() []error — so a joined error answers every question one does.
	return errors.Join(failures...)
}

// Sent returns the deliveries recorded so far, oldest first, as the caller's
// own copy — including the raw bytes, so a test that mutates what it asserts
// on cannot corrupt what a later assertion reads.
func (t *memoryTransport) Sent() []coremail.DeliveryValue {
	//: a read lock: Sent copies and mutates nothing, so several assertions in
	//: one test may run it concurrently with each other.
	t.mutex.RLock()
	defer t.mutex.RUnlock()
	//: one slot per recorded delivery.
	out := make([]coremail.DeliveryValue, 0, len(t.sent))
	//: deep enough: the envelope's recipient slice and the raw bytes both.
	for _, delivery := range t.sent {
		out = append(out, coremail.DeliveryValue{
			Envelope: coremail.EnvelopeValue{From: delivery.Envelope.From, To: slices.Clone(delivery.Envelope.To)},
			Raw:      slices.Clone(delivery.Raw),
		})
	}
	//: the caller's copy.
	return out
}

// Reset discards the record, so one test's messages are not another's.
func (t *memoryTransport) Reset() {
	t.mutex.Lock()
	defer t.mutex.Unlock()
	//: nil rather than an empty slice: the next Send reallocates at the size
	//: it needs and nothing keeps a large backing array alive.
	t.sent = nil
}
