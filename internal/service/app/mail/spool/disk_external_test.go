// Package spool_test — the durable spool: what outlives a process.
package spool_test

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
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

// TestAMailOutlivesItsProcess is kit's restart case: a mail whose first
// attempt failed is in the spool's directory when the process ends, and the
// next process delivers it — as its SECOND attempt, because the count lives
// with the mail and not with the process that is gone.
//
// Goroutine lifecycle: the first spool's consumer runs on its own goroutine
// until the case cancels it and reads its result; the second spool's is run's.
func TestAMailOutlivesItsProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(origin)
	firstRec := newRecorder()
	first := requireDiskSpool(t, spool.Config{
		Transport: &scripted{fail: relayDown}, Dir: dir, Clock: clk, Observe: firstRec.observe,
		MaxAttempts: 3, From: coremail.AddressValue{Addr: "members@example.com"},
	})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- first.Run(ctx) }()
	id, err := first.Send(context.Background(), message("Survivor"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	firstRec.expect(t, spool.EventQueued)
	firstRec.expect(t, spool.EventRetrying)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	capture := svcmail.NewCapture(0)
	secondRec := newRecorder()
	second := requireDiskSpool(t, spool.Config{
		Transport: capture, Dir: dir, Clock: clk, Observe: secondRec.observe,
		MaxAttempts: 3, From: coremail.AddressValue{Addr: "members@example.com"},
	})
	run(t, second)
	clk.BlockUntil(1)
	clk.Advance(time.Second) // the first attempt's backoff
	sent := secondRec.expect(t, spool.EventSent)
	if sent.ID != id || sent.Attempt != 2 || sent.Message.Subject != "Survivor" {
		t.Fatalf("the next process delivered %+v", sent)
	}
	if n := len(capture.Sent()); n != 1 {
		t.Fatalf("%d mails delivered", n)
	}
}

// TestAMailGivenUpKeepsWhyAfterItsProcess is issue #500 on disk: a mail one
// process gave up for a permanent refusal is read by the next process's spool
// with CodeMailRejected in its trail — the transport's SEND_REFUSED is its
// Reason and Code, so the trail is all that still says it was rejected — and
// one it gave up after its last attempt with no such code.
func TestAMailGivenUpKeepsWhyAfterItsProcess(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	clk := clock.NewManualClock(origin)
	firstRec := newRecorder()
	var calls atomic.Int32
	first := requireDiskSpool(t, spool.Config{
		Transport: &scripted{fail: func(attempt int) error {
			if calls.Add(1) == 1 {
				return refusedWith(550, "5.1.1 no such user")(attempt)
			}
			return relayDown(attempt)
		}},
		Dir: dir, Clock: clk, Observe: firstRec.observe,
		MaxAttempts: 1, From: coremail.AddressValue{Addr: "members@example.com"},
	})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- first.Run(ctx) }()
	rejected, err := first.Send(context.Background(), message("Rejected"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	firstRec.expect(t, spool.EventQueued)
	firstRec.expect(t, spool.EventDeadLettered)
	exhausted, err := first.Send(context.Background(), message("Exhausted"))
	if err != nil {
		t.Fatalf("Send() = %v", err)
	}
	firstRec.expect(t, spool.EventQueued)
	firstRec.expect(t, spool.EventDeadLettered)
	// The event is told before the queue buries the mail: both are buried
	// once the store holds them, and only then is the process stopped.
	eventuallyDeadLetters(t, first, 2)
	stop()
	if err := <-done; err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}

	second := requireDiskSpool(t, spool.Config{
		Transport: svcmail.NewCapture(0), Dir: dir, Clock: clk,
		MaxAttempts: 1, From: coremail.AddressValue{Addr: "members@example.com"},
	})
	letters, err := second.DeadLetters(context.Background(), 10)
	if err != nil || len(letters) != 2 {
		t.Fatalf("DeadLetters() = %d letters, %v", len(letters), err)
	}
	for _, letter := range letters {
		rejectedNow := slices.Contains(letter.Trail, uint32(corespool.CodeMailRejected))
		switch letter.ID {
		case rejected:
			if !rejectedNow || letter.Code != uint32(coremail.CodeSendRefused) || letter.Reason != "SEND_REFUSED" {
				t.Errorf("the rejected mail read back as %+v", letter)
			}
		case exhausted:
			if rejectedNow || slices.Contains(letter.Trail, uint32(corespool.CodeMailExpired)) {
				t.Errorf("the exhausted mail read back as %+v", letter)
			}
		default:
			t.Errorf("a dead letter of no mail sent: %+v", letter)
		}
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

// TestASendRacingCloseLandsOrIsRefused pins Close against Sends in flight:
// Close waits for a publication in progress and none starts after it, so every
// Send returns nil or SpoolClosed — never the failure of a queue closed under
// it — and closing twice is closing once.
//
// Goroutine lifecycle: one goroutine per sender, each sending until its first
// refusal; Close makes every one of them end, and the case waits for all of
// them before it reads their refusals.
func TestASendRacingCloseLandsOrIsRefused(t *testing.T) {
	t.Parallel()
	s := requireDiskSpool(t, spool.Config{
		Transport: svcmail.NewCapture(0), Dir: t.TempDir(), MaxAttempts: 3,
		From: coremail.AddressValue{Addr: "members@example.com"},
	})
	const senders int = 8
	refusals := make(chan error, senders)
	warm := make(chan struct{})
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() {
			for {
				if _, err := s.Send(context.Background(), message("Racing")); err != nil {
					refusals <- err
					return
				}
				//: Close comes once every sender is well into its stream.
				if accepted.Add(1) == int64(2*senders) {
					close(warm)
				}
			}
		})
	}
	<-warm
	closeErr := s.Close()
	wg.Wait()
	close(refusals)
	if closeErr != nil {
		t.Fatalf("Close() = %v", closeErr)
	}
	for err := range refusals {
		if !errs.HasCode(err, corespool.CodeSpoolClosed) {
			t.Errorf("a Send racing Close = %v, want nil or SpoolClosed", err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("a second Close() = %v, want nil", err)
	}
}

// TestARecordThatIsNotAMailIsDeadLettered pins MessageUndecodable: a record
// another program wrote into the spool's directory fails every attempt and
// ends as a dead letter an investigator can find by its queue identifier.
func TestARecordThatIsNotAMailIsDeadLettered(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := requireDiskSpool(t, spool.Config{Transport: svcmail.NewCapture(0), Dir: dir, MaxAttempts: 1})
	intruder, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: corequeue.PolicyValue{VisibilityTimeout: time.Minute, MaxDeliveries: 1}})
	if err != nil {
		t.Fatalf("NewFile() = %v", err)
	}
	published, err := intruder.Publish(context.Background(), []byte("not a spooled mail"))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	run(t, s)
	letters := eventuallyDeadLetters(t, s, 1)
	if letters[0].QueueID != published.ID || letters[0].ID != "" || letters[0].Reason != "MESSAGE_UNDECODABLE" || letters[0].Attempts != 1 {
		t.Fatalf("the dead letter %+v", letters[0])
	}
}

// TestARecordThatIsNotAMailIsNeverRetried pins the record's DoNotRetry: no
// attempt can decode it, so a spool with attempts to spare dead-letters it
// at its first delivery instead of retrying it until MaxAttempts (#489).
func TestARecordThatIsNotAMailIsNeverRetried(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := requireDiskSpool(t, spool.Config{Transport: svcmail.NewCapture(0), Dir: dir, MaxAttempts: 5})
	intruder, err := svcqueue.NewFile(svcqueue.FileConfig{Dir: dir, Policy: corequeue.PolicyValue{VisibilityTimeout: time.Minute, MaxDeliveries: 5}})
	if err != nil {
		t.Fatalf("NewFile() = %v", err)
	}
	published, err := intruder.Publish(context.Background(), []byte("not a spooled mail"))
	if err != nil {
		t.Fatalf("Publish() = %v", err)
	}
	run(t, s)
	letters := eventuallyDeadLetters(t, s, 1)
	if letters[0].QueueID != published.ID || letters[0].Reason != "MESSAGE_UNDECODABLE" || letters[0].Attempts != 1 {
		t.Fatalf("the dead letter %+v, want MESSAGE_UNDECODABLE after one delivery", letters[0])
	}
}
