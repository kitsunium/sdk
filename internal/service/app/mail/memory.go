package mail

import (
	"context"
	"errors"
	"slices"
	"sync"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
)

// DefaultCaptureKeep is how many deliveries [NewCapture] keeps when it is
// given a non-positive number: enough for a person to scroll a mailbox, and a
// bound on the memory a development server holds for mail nobody will read.
const DefaultCaptureKeep int = 200

// memoryTransport records what it was asked to send instead of sending it.
type memoryTransport struct {
	composer *Composer
	sent     []coremail.DeliveryValue
	mutex    sync.RWMutex
	// keep bounds the record, oldest dropped first; zero keeps everything.
	keep int
}

// newMemory is NewMemory's body: decl_gen.go writes NewMemory, from the
// design, as one call of it.
func newMemory() coremail.FullTransport {
	//: the same composer the SMTP transport builds, with the same defaults.
	return &memoryTransport{composer: NewComposer(ComposerConfig{})}
}

// newCapture is NewCapture's body: decl_gen.go writes NewCapture, from the
// design, as one call of it.
func newCapture(keep int) coremail.FullTransport {
	//: an unset bound is the default one, never "unbounded".
	if keep <= 0 {
		keep = DefaultCaptureKeep
	}
	//: the memory double, bounded.
	return &memoryTransport{composer: NewComposer(ComposerConfig{}), keep: keep}
}

// Send composes msg and records the delivery. ctx is honoured — a cancelled
// context refuses before anything is recorded — so a test exercising
// cancellation behaves the way the real transport does.
func (t *memoryTransport) Send(ctx context.Context, msg coremail.MessageValue) error {
	//: the same cancellation contract as a transport that dials.
	if ctxErr := ctx.Err(); ctxErr != nil {
		//: DialFailed: nothing was sent, and the reason is the caller's.
		return wrapAs(coremail.DialFailed, ctxErr)
	}
	envelope, envelopeErr := Envelope(msg)
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
	//: a capture keeps the newest, and forgets the oldest.
	if t.keep > 0 && len(t.sent) > t.keep {
		t.sent = slices.Delete(t.sent, 0, len(t.sent)-t.keep)
	}
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
