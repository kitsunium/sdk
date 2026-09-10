package mail_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/kitsunium/sdk/internal/core/mail"
)

// oneMethodDouble is a downstream implementation of the frozen port: exactly
// the method the interface declares, and nothing else. If Transport ever grows
// a second method this type stops compiling — which is the point, because it is
// what every consumer's own double would do, at their build rather than ours.
type oneMethodDouble struct{}

// Send satisfies the whole of [mail.Transport].
func (oneMethodDouble) Send(_ context.Context, _ mail.MessageValue) error { return nil }

// TestTransportStaysFrozenAtOneMethod is the ADR 0039 guard. pkg/v1/mail
// aliases this interface, Go interfaces are structural, and a second method
// would break every downstream implementation at compile time with no
// deprecation window.
func TestTransportStaysFrozenAtOneMethod(t *testing.T) {
	t.Parallel()
	if got := reflect.TypeFor[mail.Transport]().NumMethod(); got != 1 {
		t.Fatalf("Transport has %d methods, want 1 — a capability is a SIBLING interface, never a second method (ADR 0039)", got)
	}
	//: and a one-method downstream double still satisfies it.
	transport := mail.Transport(oneMethodDouble{})
	if transport == nil {
		t.Fatal("a one-method double does not satisfy Transport")
	}
}

// TestCapabilitiesAreSiblingsAndNotMembers pins that the two capabilities are
// reached by type assertion. A one-method double must NOT satisfy them, or the
// assertion would be answering yes for a transport that cannot do the thing.
func TestCapabilitiesAreSiblingsAndNotMembers(t *testing.T) {
	t.Parallel()
	transport := mail.Transport(oneMethodDouble{})
	if _, ok := transport.(mail.BatchSender); ok {
		t.Fatal("a one-method Transport satisfies BatchSender — the capability has leaked into the port")
	}
	if _, ok := transport.(mail.Outbox); ok {
		t.Fatal("a one-method Transport satisfies Outbox — the capability has leaked into the port")
	}
	//: and the union is exactly the four methods, so nothing was quietly added.
	if got := reflect.TypeFor[mail.FullTransport]().NumMethod(); got != 4 {
		t.Fatalf("FullTransport has %d methods, want 4 (Send + SendBatch + Sent + Reset)", got)
	}
}

// TestEnvelopeValidatesBeforeItDerives pins that a caller cannot obtain an
// envelope from a message the domain would refuse — which is what lets a
// transport call Envelope() first and trust what it gets back.
func TestEnvelopeValidatesBeforeItDerives(t *testing.T) {
	t.Parallel()
	msg := validMessage()
	msg.Subject = "x\r\nBcc: attacker@evil.example"
	if _, err := msg.Envelope(); err == nil {
		t.Fatal("Envelope() succeeded on a message carrying an injected header")
	}
}
