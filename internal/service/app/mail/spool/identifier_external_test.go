// Package spool_test — a mail queued under an identifier its caller minted:
// kept wherever the spool names the mail, refused when no mail can keep it,
// and delivered once when repeated.
package spool_test

import (
	"bytes"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/app/mail"
	"github.com/kitsunium/sdk/internal/service/app/mail/spool"
)

// heldID is an identifier a framework mints at the call, before the spool has
// the mail — a TypeID, as kit's are.
const heldID = "mail_01j8z3k4m5n6p7q8r9s0t1v2w3"

// sender is the From every spool of this file stamps.
var sender = coremail.AddressValue{Addr: "members@example.com"}

// TestACallerMintedIDIsTheMailsID pins SendWithID: the identifier the caller
// held before the spool had the mail is the one the queued and sent events,
// the attempt and the Message-ID carry, so what a framework returned at the
// call is what it reads back. A mail that brings its own Message-ID keeps it,
// and the identifier still names the mail.
func TestACallerMintedIDIsTheMailsID(t *testing.T) {
	t.Parallel()
	capture := svcmail.NewCapture(0)
	transport := &scripted{deliver: capture}
	s, _, rec := newSpool(t, spool.Config{Transport: transport, From: sender})
	if err := s.SendWithID(context.Background(), heldID, message("Held")); err != nil {
		t.Fatalf("SendWithID() = %v", err)
	}
	if queued := rec.expect(t, spool.EventQueued); queued.ID != heldID {
		t.Fatalf("queued %+v", queued)
	}
	sent := rec.expect(t, spool.EventSent)
	if sent.ID != heldID || sent.Attempt != 1 || sent.Message.MessageID != heldID+"@example.com" {
		t.Fatalf("sent %+v", sent)
	}
	messages, attempts := transport.seen()
	if len(messages) != 1 || attempts[0].ID != heldID {
		t.Fatalf("the transport saw %+v / %+v", messages, attempts)
	}
	if raw := capture.Sent()[0].Raw; !bytes.Contains(raw, []byte("<"+heldID+"@example.com>")) {
		t.Fatalf("the composed mail:\n%s", raw)
	}
	const ownID = "mail_01j8z3k4m5n6p7q8r9s0t1v2w4"
	own := message("Own")
	own.MessageID = "thread-7@example.com"
	if err := s.SendWithID(context.Background(), ownID, own); err != nil {
		t.Fatalf("SendWithID() of a mail with its own Message-ID = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	if sent := rec.expect(t, spool.EventSent); sent.ID != ownID || sent.Message.MessageID != "thread-7@example.com" {
		t.Fatalf("sent %+v", sent)
	}
}

// TestSendWithIDRefusesAnIDNoMailCanKeep pins the rule every identifier
// keeps, because it becomes the left half of a Message-ID: non-empty, at most
// MaxIDBytes, an RFC 5322 dot-atom. Anything else is InvalidMailID, nothing
// is queued, and the refusal names the rule — never the identifier, whose
// bytes are what it refuses. The longest identifier the bound admits is the
// negative control.
func TestSendWithIDRefusesAnIDNoMailCanKeep(t *testing.T) {
	t.Parallel()
	s, _, rec := newSpool(t, spool.Config{Transport: svcmail.NewCapture(0), From: sender})
	for _, c := range []struct{ name, id string }{
		{"empty", ""},
		{"longer than MaxIDBytes", strings.Repeat("a", spool.MaxIDBytes+1)},
		{"a space", "mail 7"},
		{"a line break", "mail-7\r\nBcc: evil@example.com"},
		{"a NUL", "mail-7\x00"},
		{"an at sign", "mail-7@example.com"},
		{"an angle bracket", "mail-7>"},
		{"a leading dot", ".mail-7"},
		{"a trailing dot", "mail-7."},
		{"a doubled dot", "mail..7"},
		{"a non-ASCII letter", "réunion-7"},
		{"invalid UTF-8", "mail-7\xff"},
	} {
		err := s.SendWithID(context.Background(), c.id, message("Hi"))
		if !errs.HasCode(err, spool.CodeInvalidMailID) {
			t.Errorf("%s: SendWithID() = %v, want InvalidMailID", c.name, err)
			continue
		}
		if c.id == "" {
			continue
		}
		if strings.Contains(err.Error(), c.id) {
			t.Errorf("%s: %q quotes the refused identifier", c.name, err.Error())
		}
		for _, field := range errs.FieldsOf(err) {
			if strings.Contains(field.StringValue(), c.id) {
				t.Errorf("%s: field %q carries the refused identifier", c.name, field.Key())
			}
		}
	}
	select {
	case event := <-rec.events:
		t.Fatalf("a refused identifier produced %v", event.Kind)
	default:
	}
	longest := strings.Repeat("a", spool.MaxIDBytes)
	if err := s.SendWithID(context.Background(), longest, message("Hi")); err != nil {
		t.Fatalf("SendWithID() of a %d-byte identifier = %v, want nil", spool.MaxIDBytes, err)
	}
	if queued := rec.expect(t, spool.EventQueued); queued.ID != longest {
		t.Fatalf("queued under %q", queued.ID)
	}
	rec.expect(t, spool.EventSent)
}

// TestARepeatedIDIsDeliveredOnce pins what a repeated identifier does. The
// spool does not refuse it at the call — it cannot see the identifiers in its
// queue without reading every record — and meets it at delivery with the
// ledger every redelivery meets. Repeated after its mail was delivered, the
// mail is queued, then dropped as a duplicate; repeated while the first is
// still queued, whichever the queue hands out first is sent and the other
// dropped. So a SendWithID retried after a failure that hid a landed
// publication sends the mail once.
func TestARepeatedIDIsDeliveredOnce(t *testing.T) {
	t.Parallel()
	t.Run("after its mail was delivered", func(t *testing.T) {
		t.Parallel()
		capture := svcmail.NewCapture(0)
		s, _, rec := newSpool(t, spool.Config{Transport: capture, From: sender})
		if err := s.SendWithID(context.Background(), heldID, message("Once")); err != nil {
			t.Fatalf("SendWithID() = %v", err)
		}
		rec.expect(t, spool.EventQueued)
		rec.expect(t, spool.EventSent)
		if err := s.SendWithID(context.Background(), heldID, message("Once")); err != nil {
			t.Fatalf("a repeated SendWithID() = %v, want nil: a repeat is dropped at delivery, not refused", err)
		}
		rec.expect(t, spool.EventQueued)
		if duplicate := rec.expect(t, spool.EventDuplicate); duplicate.ID != heldID || duplicate.Attempt != 1 {
			t.Fatalf("the repeat %+v", duplicate)
		}
		if n := len(capture.Sent()); n != 1 {
			t.Fatalf("the relay received %d mails, want 1", n)
		}
	})
	t.Run("while its mail is still queued", func(t *testing.T) {
		t.Parallel()
		capture := svcmail.NewCapture(0)
		rec := newRecorder()
		s, err := spool.New(spool.Config{
			Transport: capture, Clock: clock.NewManualClock(origin), Observe: rec.observe, MaxAttempts: 3, From: sender,
		})
		if err != nil {
			t.Fatalf("New() = %v", err)
		}
		for range 2 {
			if err := s.SendWithID(context.Background(), heldID, message("Twice")); err != nil {
				t.Fatalf("SendWithID() = %v", err)
			}
			rec.expect(t, spool.EventQueued)
		}
		run(t, s)
		if sent := rec.expect(t, spool.EventSent); sent.ID != heldID {
			t.Fatalf("sent %+v", sent)
		}
		if duplicate := rec.expect(t, spool.EventDuplicate); duplicate.ID != heldID {
			t.Fatalf("the repeat %+v", duplicate)
		}
		if n := len(capture.Sent()); n != 1 {
			t.Fatalf("the relay received %d mails, want 1", n)
		}
	})
}

// TestADeadLetteredIDCarriesANewAttempt pins the other half of the
// definition: an identifier repeats a mail once that mail was DELIVERED, not
// once it was seen. A mail dead-lettered under one was never delivered, so
// SendWithID queues it again under the same identifier, and the dead letter
// stays, as the evidence it is.
func TestADeadLetteredIDCarriesANewAttempt(t *testing.T) {
	t.Parallel()
	var relayUp atomic.Bool
	transport := &scripted{deliver: svcmail.NewCapture(0), fail: func(attempt int) error {
		if relayUp.Load() {
			return nil
		}
		return relayDown(attempt)
	}}
	s, _, rec := newSpool(t, spool.Config{Transport: transport, MaxAttempts: 1, From: sender})
	if err := s.SendWithID(context.Background(), heldID, message("Again")); err != nil {
		t.Fatalf("SendWithID() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	rec.expect(t, spool.EventDeadLettered)
	eventuallyDeadLetters(t, s, 1)
	relayUp.Store(true)
	if err := s.SendWithID(context.Background(), heldID, message("Again")); err != nil {
		t.Fatalf("SendWithID() under a dead-lettered identifier = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	if sent := rec.expect(t, spool.EventSent); sent.ID != heldID || sent.Attempt != 1 {
		t.Fatalf("sent %+v", sent)
	}
	if letters := eventuallyDeadLetters(t, s, 1); letters[0].ID != heldID {
		t.Fatalf("the dead letter %+v", letters[0])
	}
}
