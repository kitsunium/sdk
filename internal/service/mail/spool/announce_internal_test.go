// Package spool — the order an observer sees, pinned from inside: a delivery
// that ends while its mail's Send has not yet told the observer waits for it.
package spool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	corequeue "github.com/kitsunium/sdk/internal/core/queue"
)

// handedOver is a transport that reports the subject of each mail it accepts.
type handedOver chan string

// Send reports the mail's subject and accepts it.
func (h handedOver) Send(_ context.Context, msg coremail.MessageValue) error {
	h <- msg.Subject
	return nil
}

// TestADeliveryWaitsForItsMailsQueuedEvent pins the window a slow machine
// opens: Publish wakes the consumer, which can deliver the mail before Send
// has told the observer the mail was queued. The delivery still happens, but
// its event waits until Send is done, so the observer sees queued, then sent,
// and never a delivered mail going back to waiting.
//
// Goroutine lifecycle: one goroutine runs the delivery; the case reads its
// result once Send's registration has ended.
func TestADeliveryWaitsForItsMailsQueuedEvent(t *testing.T) {
	t.Parallel()
	events := make(chan EventKind, 4)
	transport := make(handedOver, 1)
	s, err := New(Config{Transport: transport, MaxAttempts: 3, Observe: func(e EventValue) { events <- e.Kind }})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	//: Send, held between its publication and its queued event.
	told := s.announce("mail_1")
	payload, err := json.Marshal(spooledValue{ID: "mail_1", Message: coremail.MessageValue{Subject: "Waiting"}})
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	delivered := make(chan error, 1)
	go func() {
		delivered <- s.deliver(context.Background(), corequeue.DeliveryValue{
			Message: corequeue.MessageValue{ID: "q_1", Payload: payload}, Deliveries: 1,
		})
	}()
	if subject := <-transport; subject != "Waiting" {
		t.Fatalf("the transport was handed %q", subject)
	}
	//: a bound on the wait for a report that must not come, never a verdict
	//: on how long anything takes: with the order kept, nothing arrives.
	select {
	case <-delivered:
		t.Fatal("the delivery reported before its mail's queued event")
	case kind := <-events:
		t.Fatalf("the observer heard %v before the mail was queued", kind)
	case <-time.After(50 * time.Millisecond):
	}
	s.emit(&EventValue{Kind: EventQueued, ID: "mail_1"})
	told()
	if err := <-delivered; err != nil {
		t.Fatalf("deliver() = %v", err)
	}
	if first, second := <-events, <-events; first != EventQueued || second != EventSent {
		t.Fatalf("the observer heard %v then %v, want queued then sent", first, second)
	}
}

// TestNobodyObservingRegistersNothing pins the cost side: a spool without an
// observer keeps no order, so Send registers nothing and a delivery waits for
// nothing.
func TestNobodyObservingRegistersNothing(t *testing.T) {
	t.Parallel()
	s, err := New(Config{Transport: make(handedOver, 1), MaxAttempts: 3})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	done := s.announce("mail_1")
	s.mu.RLock()
	registered := len(s.announcing)
	s.mu.RUnlock()
	done()
	if registered != 0 {
		t.Fatalf("%d registrations without an observer", registered)
	}
}
