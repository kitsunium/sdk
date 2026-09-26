// Package spool_test — a mail queued, delivered, retried on its backoff,
// dead-lettered, never sent twice, and carried across a restart.
package spool_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
	"github.com/kitsunium/sdk/internal/service/mail/spool"
)

// TestAMailIsQueuedThenDelivered pins the ordinary path: Send stamps the
// sender, the Date and a Message-ID made of the spool's identifier at the
// sender's domain, returns once the mail is queued, and the consumer hands it
// to the transport with the attempt — the identifier, the count, what
// Annotate kept — in the context.
func TestAMailIsQueuedThenDelivered(t *testing.T) {
	t.Parallel()
	capture := svcmail.NewCapture(0)
	transport := &scripted{deliver: capture}
	type traceKey struct{}
	s, clk, rec := newSpool(t, spool.Config{
		Transport: transport,
		From:      coremail.AddressValue{Name: "Members", Addr: "members@example.com"},
		Annotate: func(ctx context.Context) map[string]string {
			return map[string]string{"trace": ctx.Value(traceKey{}).(string)}
		},
	})
	id, err := s.Send(context.WithValue(context.Background(), traceKey{}, "00-trace-01"), message("Join us"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	queued := rec.expect(t, spool.EventQueued)
	if queued.ID != id || queued.Attempt != 0 || !queued.QueuedAt.Equal(clk.Now()) || queued.Meta["trace"] != "00-trace-01" {
		t.Fatalf("queued %+v", queued)
	}
	sent := rec.expect(t, spool.EventSent)
	if sent.ID != id || sent.Attempt != 1 || !sent.QueuedAt.Equal(queued.QueuedAt) {
		t.Fatalf("sent %+v", sent)
	}
	messages, attempts := transport.seen()
	if len(messages) != 1 || attempts[0].ID != id || attempts[0].Attempt != 1 || attempts[0].Meta["trace"] != "00-trace-01" {
		t.Fatalf("the transport saw %+v / %+v", messages, attempts)
	}
	stamped := messages[0]
	if stamped.From.Addr != "members@example.com" || !stamped.Date.Equal(clk.Now()) || stamped.MessageID != id+"@example.com" {
		t.Fatalf("the mail was stamped %+v", stamped)
	}
	raw := capture.Sent()[0].Raw
	if !bytes.Contains(raw, []byte("<"+id+"@example.com>")) || !bytes.Contains(raw, []byte("Subject: Join us")) {
		t.Fatalf("the composed mail:\n%s", raw)
	}
}

// TestSendRefusesWhatTheTransportWould pins Send's gate: a header carrying a
// line break, an address that is not one and an empty body are the mail
// domain's own typed refusals, at Send, and nothing is queued.
func TestSendRefusesWhatTheTransportWould(t *testing.T) {
	t.Parallel()
	s, _, rec := newSpool(t, spool.Config{Transport: svcmail.NewCapture(0), From: coremail.AddressValue{Addr: "members@example.com"}})
	injected := message("Hi\r\nBcc: evil@example.com")
	unusable := message("Hi")
	unusable.To = []coremail.AddressValue{{Addr: "not an address"}}
	empty := message("Hi")
	empty.Text = ""
	for _, c := range []struct {
		name string
		msg  coremail.MessageValue
		code errs.Code
	}{
		{"a header carrying a line break", injected, coremail.CodeHeaderInjection},
		{"an address that is not one", unusable, coremail.CodeInvalidAddress},
		{"an empty body", empty, coremail.CodeEmptyBody},
	} {
		if _, err := s.Send(context.Background(), c.msg); !errs.HasCode(err, c.code) {
			t.Errorf("%s: Send() = %v, want %v", c.name, err, c.code)
		}
	}
	select {
	case event := <-rec.events:
		t.Fatalf("a refused mail produced %v", event.Kind)
	default:
	}
}

// TestSendRefusesAnEmptyIdentifier pins the guard on Config.NewID: the spool
// drops a mail whose identifier it delivered already, so an empty identifier
// would make every later empty one a redelivery. Send refuses it, and nothing
// is queued.
func TestSendRefusesAnEmptyIdentifier(t *testing.T) {
	t.Parallel()
	s, _, rec := newSpool(t, spool.Config{
		Transport: svcmail.NewCapture(0),
		From:      coremail.AddressValue{Addr: "members@example.com"},
		NewID:     func() (string, error) { return "", nil },
	})
	if _, err := s.Send(context.Background(), message("Hi")); !errs.HasCode(err, spool.CodeSpoolMisconfigured) {
		t.Fatalf("Send() with an empty identifier = %v, want SpoolMisconfigured", err)
	}
	select {
	case event := <-rec.events:
		t.Fatalf("a refused mail produced %v", event.Kind)
	default:
	}
}

// TestRetriesOnTheBackoffThenDeadLetters is kit's failing-relay case: each
// attempt fails, the next waits a doubling backoff on the spool's clock — not
// a nanosecond early — and the last one dead-letters the mail with its
// failure, readable with every field an investigator needs.
func TestRetriesOnTheBackoffThenDeadLetters(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: relayDown}
	s, clk, rec := newSpool(t, spool.Config{Transport: transport, From: coremail.AddressValue{Addr: "members@example.com"}})
	id, err := s.Send(context.Background(), message("Hello"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	queued := rec.expect(t, spool.EventQueued)
	first := rec.expect(t, spool.EventRetrying)
	if first.Attempt != 1 || first.Next.Sub(first.At) != time.Second || !errs.HasCode(first.Err, svcmail.CodeDialFailed) {
		t.Fatalf("the first retry %+v", first)
	}
	notEarly(t, clk, transport, time.Second, 1)
	second := rec.expect(t, spool.EventRetrying)
	if second.Attempt != 2 || second.Next.Sub(second.At) != 2*time.Second || !second.QueuedAt.Equal(queued.QueuedAt) {
		t.Fatalf("the second retry %+v", second)
	}
	notEarly(t, clk, transport, 2*time.Second, 2)
	dead := rec.expect(t, spool.EventDeadLettered)
	if dead.Attempt != 3 || dead.ID != id {
		t.Fatalf("the dead letter event %+v", dead)
	}
	letters := eventuallyDeadLetters(t, s, 1)
	letter := letters[0]
	if letter.ID != id || letter.Attempts != 3 || letter.Reason != "DIAL_FAILED" || letter.Code != uint32(svcmail.CodeDialFailed) ||
		letter.Cause != errs.PublicOf(svcmail.DialFailed) || letter.Message.Subject != "Hello" || !letter.QueuedAt.Equal(queued.QueuedAt) {
		t.Fatalf("the dead letter %+v", letter)
	}
	messages, _ := transport.seen()
	if len(messages) != 3 {
		t.Fatalf("%d attempts, want 3", len(messages))
	}
	//: every attempt carried the same Message-ID, so a receiver can tell a
	//: retry from a new mail.
	for _, m := range messages {
		if m.MessageID != messages[0].MessageID || m.MessageID == "" {
			t.Fatalf("the Message-ID changed across attempts: %q vs %q", m.MessageID, messages[0].MessageID)
		}
	}
}

// drivenClock is what notEarly drives a ManualClock through.
type drivenClock interface {
	Advance(d time.Duration)
	BlockUntil(n int)
}

// notEarly advances clk to one nanosecond before delay and checks the
// transport has still seen only before attempts, then to delay.
func notEarly(t *testing.T, clk drivenClock, transport *scripted, delay time.Duration, before int) {
	t.Helper()
	clk.BlockUntil(1)
	clk.Advance(delay - time.Nanosecond)
	clk.BlockUntil(1) // the consumer found nothing receivable and waits again
	if messages, _ := transport.seen(); len(messages) != before {
		t.Fatalf("an attempt came before its backoff of %s: %d attempts", delay, len(messages))
	}
	clk.Advance(time.Nanosecond)
}

// eventuallyDeadLetters reads the spool's dead letters until there are want
// of them: the nack that records one runs after the event is emitted.
func eventuallyDeadLetters(t *testing.T, s *spool.Spool, want int) []spool.DeadLetterValue {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		letters, err := s.DeadLetters(context.Background(), 10)
		if err != nil {
			t.Fatalf("DeadLetters() = %v", err)
		}
		if len(letters) == want {
			return letters
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d dead letters, want %d", len(letters), want)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestADeliveredMailIsNeverSentTwice pins the ledger: a transport slow enough
// that the lease lapses while it accepts the mail gets it back from the queue
// afterwards, and the spool drops that redelivery instead of sending it again.
func TestADeliveredMailIsNeverSentTwice(t *testing.T) {
	t.Parallel()
	capture := svcmail.NewCapture(0)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	slow := lateRelay{capture: capture, entered: entered, release: release}
	s, clk, rec := newSpool(t, spool.Config{Transport: slow, SendTimeout: 5 * time.Second, From: coremail.AddressValue{Addr: "members@example.com"}})
	id, err := s.Send(context.Background(), message("Once"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	<-entered
	clk.Advance(10 * time.Second) // past the lease: twice SendTimeout
	close(release)
	if sent := rec.expect(t, spool.EventSent); sent.ID != id || sent.Attempt != 1 {
		t.Fatalf("sent %+v", sent)
	}
	if duplicate := rec.expect(t, spool.EventDuplicate); duplicate.ID != id || duplicate.Attempt != 2 {
		t.Fatalf("the redelivery %+v", duplicate)
	}
	if n := len(capture.Sent()); n != 1 {
		t.Fatalf("the relay received %d mails, want 1", n)
	}
}

// lateRelay is a relay that ignores its caller's deadline: the first mail it
// is handed, it accepts only once released — long after the lease lapsed.
type lateRelay struct {
	capture coremail.Transport
	entered chan struct{}
	release chan struct{}
}

// Send accepts the mail, late the first time.
func (l lateRelay) Send(ctx context.Context, msg coremail.MessageValue) error {
	if attempt, _ := spool.AttemptFrom(ctx); attempt.Attempt == 1 {
		l.entered <- struct{}{}
		<-l.release
	}
	return l.capture.Send(context.WithoutCancel(ctx), msg)
}

// TestAnAttemptIsBoundedBySendTimeout pins the bound on the spool's clock: a
// transport still talking at SendTimeout has its context cancelled, and the
// attempt fails like any other.
func TestAnAttemptIsBoundedBySendTimeout(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{}, 1)
	hanging := &scripted{fail: func(int) error { return nil }, deliver: hangingTransport{entered: entered}}
	s, clk, rec := newSpool(t, spool.Config{Transport: hanging, SendTimeout: 5 * time.Second, From: coremail.AddressValue{Addr: "members@example.com"}})
	if _, err := s.Send(context.Background(), message("Slow")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	<-entered
	clk.Advance(5 * time.Second)
	retry := rec.expect(t, spool.EventRetrying)
	if !errors.Is(retry.Err, context.Canceled) {
		t.Fatalf("the timed-out attempt failed with %v", retry.Err)
	}
}

// hangingTransport waits for its context to end.
type hangingTransport struct {
	entered chan struct{}
}

// Send announces itself and waits.
func (h hangingTransport) Send(ctx context.Context, _ coremail.MessageValue) error {
	h.entered <- struct{}{}
	<-ctx.Done()
	return ctx.Err()
}

// TestAPanickingTransportFailsTheAttempt pins the recovery: the panic is the
// attempt's failure — TransportPanicked, with a Public text that quotes
// nothing — and the mail is retried like any other.
func TestAPanickingTransportFailsTheAttempt(t *testing.T) {
	t.Parallel()
	panicking := &scripted{fail: func(int) error { panic("canary: do-not-leak-7c2e") }}
	s, _, rec := newSpool(t, spool.Config{Transport: panicking, From: coremail.AddressValue{Addr: "members@example.com"}})
	if _, err := s.Send(context.Background(), message("Boom")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	retry := rec.expect(t, spool.EventRetrying)
	if !errs.HasCode(retry.Err, spool.CodeTransportPanicked) || strings.Contains(errs.PublicOf(retry.Err), "do-not-leak") {
		t.Fatalf("the panicking attempt failed with %v (%q)", retry.Err, errs.PublicOf(retry.Err))
	}
}

// TestNewRefuses pins every configuration New refuses: no transport, no
// attempt budget, a negative bound, a default sender that is not an address.
func TestNewRefuses(t *testing.T) {
	t.Parallel()
	transport := svcmail.NewCapture(0)
	for _, c := range []struct {
		name string
		cfg  spool.Config
	}{
		{"no transport", spool.Config{MaxAttempts: 3}},
		{"no attempt budget", spool.Config{Transport: transport}},
		{"a negative bound", spool.Config{Transport: transport, MaxAttempts: 3, MaxMessageBytes: -1}},
		{"a sender that is not an address", spool.Config{Transport: transport, MaxAttempts: 3, From: coremail.AddressValue{Addr: "not an address"}}},
	} {
		if s, err := spool.New(c.cfg); !errs.HasCode(err, spool.CodeSpoolMisconfigured) || s != nil {
			t.Errorf("%s: New() = %v, %v", c.name, s, err)
		}
	}
}

// TestAClosedSpoolRefusesMail pins Close: every later Send is SpoolClosed.
func TestAClosedSpoolRefusesMail(t *testing.T) {
	t.Parallel()
	s, err := spool.New(spool.Config{Transport: svcmail.NewCapture(0), MaxAttempts: 3})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	msg := message("Late")
	msg.From = coremail.AddressValue{Addr: "members@example.com"}
	if _, err := s.Send(context.Background(), msg); !errs.HasCode(err, spool.CodeSpoolClosed) {
		t.Fatalf("Send() after Close = %v, want SpoolClosed", err)
	}
}

// TestAMailLargerThanTheBoundIsRefused pins MaxMessageBytes: the queue's own
// MessageTooLarge, at Send, and nothing queued.
func TestAMailLargerThanTheBoundIsRefused(t *testing.T) {
	t.Parallel()
	s, _, _ := newSpool(t, spool.Config{Transport: svcmail.NewCapture(0), MaxMessageBytes: 512, From: coremail.AddressValue{Addr: "members@example.com"}})
	big := message("Big")
	big.Text = strings.Repeat("x", 1024)
	if _, err := s.Send(context.Background(), big); !errs.HasReason(err, "MESSAGE_TOO_LARGE") {
		t.Fatalf("Send() of an oversized mail = %v, want MESSAGE_TOO_LARGE", err)
	}
}

// TestEventKinds pins the kinds' names, and "unknown" for a value the package
// never mints.
func TestEventKinds(t *testing.T) {
	t.Parallel()
	for kind, want := range map[spool.EventKind]string{
		spool.EventQueued: "queued", spool.EventSent: "sent", spool.EventRetrying: "retrying",
		spool.EventDeadLettered: "dead-lettered", spool.EventDuplicate: "duplicate", spool.EventKind(0): "unknown",
	} {
		if got := kind.String(); got != want {
			t.Errorf("%d renders %q, want %q", kind, got, want)
		}
	}
	if _, ok := spool.AttemptFrom(context.Background()); ok {
		t.Error("a context the spool never handed out carries an attempt")
	}
}
