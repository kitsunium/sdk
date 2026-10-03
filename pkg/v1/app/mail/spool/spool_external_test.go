// Package spool_test — the durable outbox through its public names.
package spool_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/app/id"
	"github.com/kitsunium/sdk/pkg/v1/app/mail"
	"github.com/kitsunium/sdk/pkg/v1/app/mail/spool"
)

// attemptRecorder is a transport that notes the attempt its context carries
// and hands the mail on.
type attemptRecorder struct {
	next     mail.Transport
	attempts chan spool.Attempt
}

// Send records the attempt, then delivers.
func (a attemptRecorder) Send(ctx context.Context, msg mail.Message) error {
	attempt, _ := spool.AttemptFrom(ctx)
	a.attempts <- attempt
	return a.next.Send(ctx, msg)
}

// TestTheSpoolThroughTheFacade pins the outbox through public names: Send
// queues and returns the spool's identifier, Run delivers it through the
// transport with the attempt in the context, the observer is told, and the
// capture transport keeps the composed mail under the stable Message-ID.
//
// Goroutine lifecycle: one goroutine carries Run; cancelling ctx ends it, and
// the test reads its result from done before closing the spool.
func TestTheSpoolThroughTheFacade(t *testing.T) {
	t.Parallel()
	capture := mail.NewCapture(0)
	attempts := make(chan spool.Attempt, 4)
	events := make(chan spool.Event, 8)
	outbox, err := spool.New(spool.Config{
		Transport:   attemptRecorder{next: capture, attempts: attempts},
		MaxAttempts: 3,
		From:        mail.Address{Name: "App", Addr: "app@example.com"},
		Observe:     func(e spool.Event) { events <- e },
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- outbox.Run(ctx) }()
	id, err := outbox.Send(context.Background(), mail.Message{
		To: []mail.Address{{Addr: "user@example.net"}}, Subject: "Welcome", Text: "Hello",
	})
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	if attempt := <-attempts; attempt.ID != id || attempt.Attempt != 1 {
		t.Fatalf("the transport saw %+v", attempt)
	}
	for _, want := range []spool.EventKind{spool.EventQueued, spool.EventSent} {
		if e := <-events; e.Kind != want || e.ID != id {
			t.Fatalf("event %v for %s, want %v for %s", e.Kind, e.ID, want, id)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	sent := capture.Sent()
	if len(sent) != 1 || !strings.Contains(string(sent[0].Raw), "<"+id+"@example.com>") {
		t.Fatalf("the capture kept %d mails", len(sent))
	}
	if _, err := spool.New(spool.Config{Transport: capture}); !errors.Is(err, spool.Misconfigured) {
		t.Fatalf("a spool without an attempt budget = %v, want SpoolMisconfigured", err)
	}
}

// mintedIDs returns one identifier from every generator pkg/v1/app/id offers,
// the TypeID under the longest prefix a TypeID allows among them.
func mintedIDs(t *testing.T) []string {
	t.Helper()
	longest, err := id.NewTypeID(strings.Repeat("m", 63))
	if err != nil {
		t.Fatalf("NewTypeID() = %v", err)
	}
	var minted []string
	for _, mint := range []func() (string, error){id.UUIDv4, id.UUIDv7, id.ULID, id.Snowflake, id.NanoID, id.KSUID, longest.New} {
		newID, err := mint()
		if err != nil {
			t.Fatalf("minting an identifier = %v", err)
		}
		minted = append(minted, newID)
	}
	return minted
}

// TestSendWithIDThroughTheFacade pins the caller's identifier through public
// names: every identifier pkg/v1/app/id mints is one a spool keeps, the events
// carry it and the Message-ID is made of it, and an identifier no mail can
// keep — not a dot-atom, or longer than MaxIDBytes — is InvalidMailID
// for errors.Is.
//
// Goroutine lifecycle: one goroutine carries Run, started once every mail is
// queued; cancelling ctx ends it, and the test reads its result from done
// before closing the spool.
func TestSendWithIDThroughTheFacade(t *testing.T) {
	t.Parallel()
	capture := mail.NewCapture(0)
	events := make(chan spool.Event, 32)
	outbox, err := spool.New(spool.Config{
		Transport:   capture,
		MaxAttempts: 3,
		From:        mail.Address{Name: "App", Addr: "app@example.com"},
		Observe:     func(e spool.Event) { events <- e },
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	msg := mail.Message{To: []mail.Address{{Addr: "user@example.net"}}, Subject: "Held", Text: "Hello"}
	for _, bad := range []string{"not an id", strings.Repeat("a", spool.MaxIDBytes+1)} {
		if err := outbox.SendWithID(context.Background(), bad, msg); !errors.Is(err, spool.InvalidMailID) {
			t.Fatalf("SendWithID(%.12q…) = %v, want InvalidMailID", bad, err)
		}
	}
	minted := mintedIDs(t)
	for _, outboxID := range minted {
		if err := outbox.SendWithID(context.Background(), outboxID, msg); err != nil {
			t.Fatalf("SendWithID(%q) = %v", outboxID, err)
		}
		if e := <-events; e.Kind != spool.EventQueued || e.ID != outboxID {
			t.Fatalf("event %v for %s, want queued for %s", e.Kind, e.ID, outboxID)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- outbox.Run(ctx) }()
	sentIDs := map[string]bool{}
	for range minted {
		e := <-events
		if e.Kind != spool.EventSent || e.Message.MessageID != e.ID+"@example.com" {
			t.Fatalf("event %v for %s with Message-ID %q, want sent under its identifier", e.Kind, e.ID, e.Message.MessageID)
		}
		sentIDs[e.ID] = true
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if err := outbox.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	for _, outboxID := range minted {
		if !sentIDs[outboxID] {
			t.Fatalf("%s was never sent", outboxID)
		}
	}
	if n := len(capture.Sent()); n != len(minted) {
		t.Fatalf("the capture kept %d mails, want %d", n, len(minted))
	}
}
