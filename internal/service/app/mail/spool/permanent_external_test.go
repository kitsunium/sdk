// Package spool_test — a permanent refusal dead-lettered at once, a temporary
// one retried, and a mail that outlived Config.MaxAge given up.
package spool_test

import (
	"context"
	"errors"
	"net/textproto"
	"slices"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/app/mail"
	corespool "github.com/kitsunium/sdk/internal/core/app/mail/spool"
	corequeue "github.com/kitsunium/sdk/internal/core/data/queue"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	svcmail "github.com/kitsunium/sdk/internal/service/app/mail"
	"github.com/kitsunium/sdk/internal/service/app/mail/spool"
	svcqueue "github.com/kitsunium/sdk/internal/service/data/queue"
)

// refusedWith is the failure the SDK's SMTP transport returns for a refused
// RCPT: SendRefused as the origin, the server's reply as its source.
func refusedWith(code int, msg string) func(int) error {
	return refusedAt("RCPT", code, msg)
}

// refusedAt is the same failure for the SMTP command it names.
func refusedAt(command string, code int, msg string) func(int) error {
	return func(int) error {
		sentinel := coremail.SendRefused
		return errs.Wrap(&textproto.Error{Code: code, Msg: msg}, errs.WrapParams{
			Code: sentinel.Code(), Reason: sentinel.Reason(), Public: sentinel.Public(),
			Private: sentinel.Private(), ExitCode: sentinel.ExitCode(),
		}, errs.String("command", command))
	}
}

// TestA552ToRCPTIsTemporary pins RFC 5321 §4.5.3.1.10: a 552 to RCPT is read
// as 452, too many recipients, and retried with its reply still reachable,
// while a 552 to the end of DATA — the size limit — is permanent.
func TestA552ToRCPTIsTemporary(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: refusedWith(552, "5.5.3 too many recipients")}
	s, _, rec := newSpool(t, spool.Config{Transport: transport, From: sender})
	if _, err := s.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	retrying := rec.expect(t, spool.EventRetrying)
	if reply, ok := errors.AsType[*textproto.Error](retrying.Err); !ok || reply.Code != 552 {
		t.Fatalf("the reply of %v = (%v, %v), want 552", retrying.Err, reply, ok)
	}

	sized := &scripted{fail: refusedAt("DATA-END", 552, "5.3.4 message too big")}
	s2, _, rec2 := newSpool(t, spool.Config{Transport: sized, From: sender})
	if _, err := s2.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec2.expect(t, spool.EventQueued)
	if dead := rec2.expect(t, spool.EventDeadLettered); dead.Attempt != 1 || !errs.HasCode(dead.Err, corespool.CodeMailRejected) {
		t.Fatalf("the dead letter event %+v", dead)
	}
}

// TestAPermanentRefusalIsDeadLetteredAtOnce pins RFC 5321 §4.2.1: a 5xx reply
// is not repeated. The mail is dead-lettered on its first attempt, with
// attempts left, the transport's verdict as the dead letter's reason and
// CodeMailRejected in its trail (issue #500), and the event's error marked
// CodeMailRejected with the reply still reachable.
func TestAPermanentRefusalIsDeadLetteredAtOnce(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: refusedWith(550, "5.1.1 no such user")}
	s, _, rec := newSpool(t, spool.Config{Transport: transport, From: sender, MaxAttempts: 5})
	id, err := s.Send(context.Background(), message("Hello"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	dead := rec.expect(t, spool.EventDeadLettered)
	if dead.Attempt != 1 || dead.ID != id || !errs.HasCode(dead.Err, corespool.CodeMailRejected) ||
		!errors.Is(dead.Err, coremail.SendRefused) {
		t.Fatalf("the dead letter event %+v", dead)
	}
	if reply, ok := errors.AsType[*textproto.Error](dead.Err); !ok || reply.Code != 550 {
		t.Fatalf("the reply of %v = (%v, %v), want 550", dead.Err, reply, ok)
	}
	letter := eventuallyDeadLetters(t, s, 1)[0]
	if letter.ID != id || letter.Attempts != 1 || letter.Reason != "SEND_REFUSED" ||
		letter.Code != uint32(coremail.CodeSendRefused) || letter.Cause != errs.PublicOf(coremail.SendRefused) ||
		!slices.Contains(letter.Trail, uint32(corespool.CodeMailRejected)) {
		t.Fatalf("the dead letter %+v", letter)
	}
	if messages, _ := transport.seen(); len(messages) != 1 {
		t.Fatalf("%d attempts, want 1", len(messages))
	}
}

// TestAConnectorsBareReplyIsRecognised pins the other shape: a connector that
// returns the reply as it is gets it classified too, and its dead letter then
// records MailRejected, which says what happened.
func TestAConnectorsBareReplyIsRecognised(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: func(int) error { return &textproto.Error{Code: 554, Msg: "5.7.1 rejected"} }}
	s, _, rec := newSpool(t, spool.Config{Transport: transport, From: sender, MaxAttempts: 5})
	if _, err := s.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	dead := rec.expect(t, spool.EventDeadLettered)
	if !errors.Is(dead.Err, corespool.MailRejected) {
		t.Fatalf("the dead letter event's error %v, want MailRejected as its origin", dead.Err)
	}
	if reply, ok := errors.AsType[*textproto.Error](dead.Err); !ok || reply.Code != 554 {
		t.Fatalf("the reply of %v = (%v, %v), want 554", dead.Err, reply, ok)
	}
	letter := eventuallyDeadLetters(t, s, 1)[0]
	if letter.Attempts != 1 || letter.Reason != "MAIL_REJECTED" || letter.Code != uint32(corespool.CodeMailRejected) {
		t.Fatalf("the dead letter %+v", letter)
	}
}

// TestATemporaryRefusalIsRetried pins the other class: a 4xx reply waits its
// backoff like a relay that is down.
func TestATemporaryRefusalIsRetried(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: refusedWith(450, "4.2.1 mailbox busy")}
	s, _, rec := newSpool(t, spool.Config{Transport: transport, From: sender})
	if _, err := s.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	retrying := rec.expect(t, spool.EventRetrying)
	if retrying.Attempt != 1 || errs.HasCode(retrying.Err, corespool.CodeMailRejected) {
		t.Fatalf("the retry %+v", retrying)
	}
}

// TestRetryPermanentRetriesA5xx pins the escape hatch: with RetryPermanent a
// 5xx waits its backoff, and is dead-lettered only after the last attempt.
func TestRetryPermanentRetriesA5xx(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: refusedWith(550, "5.1.1 no such user")}
	s, clk, rec := newSpool(t, spool.Config{Transport: transport, From: sender, MaxAttempts: 2, RetryPermanent: true})
	if _, err := s.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	if first := rec.expect(t, spool.EventRetrying); first.Attempt != 1 {
		t.Fatalf("the retry %+v", first)
	}
	notEarly(t, clk, transport, time.Second, 1)
	dead := rec.expect(t, spool.EventDeadLettered)
	if dead.Attempt != 2 || errs.HasCode(dead.Err, corespool.CodeMailRejected) {
		t.Fatalf("the dead letter event %+v", dead)
	}
	if letter := eventuallyDeadLetters(t, s, 1)[0]; letter.Attempts != 2 || letter.Reason != "SEND_REFUSED" {
		t.Fatalf("the dead letter %+v", letter)
	}
}

// TestAMailOlderThanMaxAgeIsGivenUp pins Config.MaxAge on the manual clock: a
// failure before the age is retried, the first failure at or after it
// dead-letters the mail with that failure, marked CodeMailExpired — in the
// dead letter's trail too (issue #500) —, though attempts are left.
func TestAMailOlderThanMaxAgeIsGivenUp(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: relayDown}
	s, clk, rec := newSpool(t, spool.Config{Transport: transport, From: sender, MaxAttempts: 10, MaxAge: 3 * time.Second})
	id, err := s.Send(context.Background(), message("Hello"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	rec.expect(t, spool.EventRetrying) // at 0s, next at 1s
	notEarly(t, clk, transport, time.Second, 1)
	second := rec.expect(t, spool.EventRetrying) // at 1s, next at 3s
	if errs.HasCode(second.Err, corespool.CodeMailExpired) {
		t.Fatalf("a mail younger than MaxAge was given up: %+v", second)
	}
	notEarly(t, clk, transport, 2*time.Second, 2)
	dead := rec.expect(t, spool.EventDeadLettered) // at 3s: MaxAge reached
	if dead.Attempt != 3 || dead.ID != id || !errs.HasCode(dead.Err, corespool.CodeMailExpired) ||
		!errs.HasCode(dead.Err, coremail.CodeDialFailed) || dead.At.Sub(dead.QueuedAt) != 3*time.Second {
		t.Fatalf("the dead letter event %+v", dead)
	}
	letter := eventuallyDeadLetters(t, s, 1)[0]
	if letter.Attempts != 3 || letter.Reason != "DIAL_FAILED" || letter.Code != uint32(coremail.CodeDialFailed) ||
		!slices.Contains(letter.Trail, uint32(corespool.CodeMailExpired)) || slices.Contains(letter.Trail, uint32(corespool.CodeMailRejected)) {
		t.Fatalf("the dead letter %+v", letter)
	}
}

// TestAZeroMaxAgeSetsNoAge pins the zero: without MaxAge, MaxAttempts alone
// decides, however old the mail — a failure a year after the Send is retried.
func TestAZeroMaxAgeSetsNoAge(t *testing.T) {
	t.Parallel()
	transport := &scripted{fail: relayDown}
	s, clk, rec := newSpool(t, spool.Config{Transport: transport, From: sender})
	if _, err := s.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	rec.expect(t, spool.EventRetrying)
	clk.BlockUntil(1)
	clk.Advance(365 * 24 * time.Hour)
	if second := rec.expect(t, spool.EventRetrying); second.Attempt != 2 {
		t.Fatalf("the second retry %+v", second)
	}
}

// noReject is a caller's queue without the Rejecter sibling: the memory
// broker's Broker, Waker and LeaseExtender, and nothing else.
type noReject struct {
	corequeue.Broker
}

// Wake forwards the memory broker's wake, so the consumer sleeps on it.
func (b *noReject) Wake() corequeue.WakeValue { return b.Broker.(corequeue.Waker).Wake() }

// Extend forwards the memory broker's lease extension, which parks a retry.
func (b *noReject) Extend(ctx context.Context, receipt corequeue.ReceiptValue, by time.Duration) (corequeue.LeaseValue, error) {
	return b.Broker.(corequeue.LeaseExtender).Extend(ctx, receipt, by)
}

// TestABrokerThatCannotRejectRetriesAPermanentRefusal pins the fallback: a
// Config.Broker that cannot dead-letter early gets a 5xx parked like any
// failure, and the mail dead-lettered after its last attempt — never an
// EventDeadLettered for a mail the queue then hands back.
func TestABrokerThatCannotRejectRetriesAPermanentRefusal(t *testing.T) {
	t.Parallel()
	clk := clock.NewManualClock(origin)
	memory, err := svcqueue.NewMemory(svcqueue.MemoryConfig{Clock: clk, Policy: corequeue.PolicyValue{
		VisibilityTimeout: 2 * spool.DefaultSendTimeout, RetryDelay: time.Second, MaxDeliveries: 2, MaxMessageBytes: 1 << 20,
	}})
	if err != nil {
		t.Fatalf("NewMemory() = %v", err)
	}
	rec := newRecorder()
	transport := &scripted{fail: refusedWith(550, "5.1.1 no such user")}
	s, err := spool.New(spool.Config{
		Transport: transport, From: sender, Broker: &noReject{Broker: memory}, MaxAttempts: 2,
		Clock: clk, Observe: rec.observe,
	})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	run(t, s)
	if _, err := s.Send(context.Background(), message("Hello")); err != nil {
		t.Fatalf("Send() = %v", err)
	}
	rec.expect(t, spool.EventQueued)
	if first := rec.expect(t, spool.EventRetrying); first.Attempt != 1 || errs.HasCode(first.Err, corespool.CodeMailRejected) {
		t.Fatalf("the retry %+v", first)
	}
	notEarly(t, clk, transport, time.Second, 1)
	if dead := rec.expect(t, spool.EventDeadLettered); dead.Attempt != 2 {
		t.Fatalf("the dead letter event %+v", dead)
	}
}

// TestNewRefusesANegativeMaxAge pins the refusal of an age that is no age.
func TestNewRefusesANegativeMaxAge(t *testing.T) {
	t.Parallel()
	cfg := spool.Config{Transport: svcmail.NewCapture(0), MaxAttempts: 3, MaxAge: -time.Second}
	if s, err := spool.New(cfg); !errs.HasCode(err, corespool.CodeSpoolMisconfigured) || s != nil {
		t.Fatalf("New() = %v, %v", s, err)
	}
}
