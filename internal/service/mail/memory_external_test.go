package mail_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
)

// TestMemoryTransportComposesRatherThanRecording is what makes the double a
// double. A stub that kept the Message value would accept every message the
// SMTP transport refuses, and a consumer's suite would go green right up to
// production.
func TestMemoryTransportComposesRatherThanRecording(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	msg := simpleMessage()
	msg.Subject = "hi\r\nBcc: attacker@evil.example"
	if err := transport.Send(context.Background(), msg); !errs.HasCode(err, coremail.CodeHeaderInjection) {
		t.Fatalf("Send = %v, want CodeHeaderInjection — the double must refuse what production refuses", err)
	}
	if sent := transport.Sent(); len(sent) != 0 {
		t.Fatalf("the refused message was recorded anyway: %d deliveries", len(sent))
	}
}

// TestMemoryTransportRecordsTheComposedBytes pins that what a consumer asserts
// on is what would have gone on the wire — not the Message it built a moment
// earlier, which can only tell it what it already knows.
func TestMemoryTransportRecordsTheComposedBytes(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	msg := simpleMessage()
	msg.Subject = "Réunion à 9h"
	msg.Bcc = []coremail.AddressValue{{Addr: "blind@fake.example"}}
	if err := transport.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	sent := transport.Sent()
	if len(sent) != 1 {
		t.Fatalf("recorded %d deliveries, want 1", len(sent))
	}
	raw := string(sent[0].Raw)
	//: the subject is encoded, so a consumer asserting on the raw bytes sees
	//: the encoded-word rather than the string it typed — which is the point.
	if !strings.Contains(raw, "=?utf-8?q?R=C3=A9union_=C3=A0_9h?=") {
		t.Fatalf("the recorded bytes do not carry the RFC 2047 subject:\n%s", raw)
	}
	//: and the Bcc is in the envelope and nowhere in the bytes.
	if strings.Contains(raw, "blind@fake.example") {
		t.Fatalf("the recorded bytes disclose a Bcc recipient:\n%s", raw)
	}
	if len(sent[0].Envelope.To) != 2 || sent[0].Envelope.To[1] != "blind@fake.example" {
		t.Fatalf("envelope.To = %v, want the Bcc recipient", sent[0].Envelope.To)
	}
}

// TestMemoryTransportReturnsACopy pins that a test mutating what it asserts on
// cannot corrupt what a later assertion reads.
func TestMemoryTransportReturnsACopy(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	if err := transport.Send(context.Background(), simpleMessage()); err != nil {
		t.Fatalf("Send = %v, want nil", err)
	}
	first := transport.Sent()
	first[0].Raw[0] = 'X'
	first[0].Envelope.To[0] = "hijacked@evil.example"
	second := transport.Sent()
	if second[0].Raw[0] == 'X' || second[0].Envelope.To[0] == "hijacked@evil.example" {
		t.Fatal("Sent() shares its backing arrays with the transport's own record")
	}
}

// TestMemoryTransportIsTheFullUnion pins that the double implements every
// sibling, so a consumer can wire it wherever a batching transport is expected.
func TestMemoryTransportIsTheFullUnion(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	if err := transport.SendBatch(context.Background(), []coremail.MessageValue{simpleMessage(), simpleMessage()}); err != nil {
		t.Fatalf("SendBatch = %v, want nil", err)
	}
	if got := len(transport.Sent()); got != 2 {
		t.Fatalf("recorded %d deliveries, want 2", got)
	}
	transport.Reset()
	if got := len(transport.Sent()); got != 0 {
		t.Fatalf("Reset left %d deliveries", got)
	}
}

// TestMemoryBatchAggregatesRatherThanShortCircuits pins that one bad message
// does not silence the rest — the reason SendBatch is not a loop in the caller.
func TestMemoryBatchAggregatesRatherThanShortCircuits(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	bad := simpleMessage()
	bad.To = nil
	worse := simpleMessage()
	worse.Subject = "x\r\nBcc: attacker@evil.example"
	err := transport.SendBatch(context.Background(), []coremail.MessageValue{bad, simpleMessage(), worse, simpleMessage()})
	if err == nil {
		t.Fatal("SendBatch = nil, want the two failures joined")
	}
	//: errs.HasCode walks Unwrap() []error, so a joined error answers the same
	//: questions a single one does.
	if !errs.HasCode(err, coremail.CodeNoRecipients) || !errs.HasCode(err, coremail.CodeHeaderInjection) {
		t.Fatalf("SendBatch = %v, want both verdicts reachable in the join", err)
	}
	if got := len(transport.Sent()); got != 2 {
		t.Fatalf("recorded %d deliveries, want 2 — the good messages must still go", got)
	}
}

// TestMemoryTransportIsSafeForConcurrentUse exercises the mutex under -race,
// which is the only way the claim in the doc comment means anything.
func TestMemoryTransportIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	var group sync.WaitGroup
	//: eight writers and eight readers over one transport, under -race.
	for range 8 {
		group.Go(func() {
			for range 20 {
				if sendErr := transport.Send(context.Background(), simpleMessage()); sendErr != nil {
					t.Errorf("Send = %v, want nil", sendErr)
					return
				}
				if len(transport.Sent()) == 0 {
					t.Error("Sent() returned nothing after a Send")
					return
				}
			}
		})
	}
	group.Wait()
	if got := len(transport.Sent()); got != 160 {
		t.Fatalf("recorded %d deliveries, want 160", got)
	}
}

// TestMemoryTransportHonoursACancelledContext keeps the double behaving like
// the transport it stands in for.
func TestMemoryTransportHonoursACancelledContext(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.Send(ctx, simpleMessage()); !errs.HasCode(err, svcmail.CodeDialFailed) {
		t.Fatalf("Send(cancelled) = %v, want CodeDialFailed", err)
	}
}
