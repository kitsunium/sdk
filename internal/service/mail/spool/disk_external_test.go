// Package spool_test — the durable spool: what outlives a process.
package spool_test

import (
	"context"
	"testing"
	"time"

	coremail "github.com/kitsunium/sdk/internal/core/mail"
	corequeue "github.com/kitsunium/sdk/internal/core/queue"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	svcmail "github.com/kitsunium/sdk/internal/service/mail"
	"github.com/kitsunium/sdk/internal/service/mail/spool"
	svcqueue "github.com/kitsunium/sdk/internal/service/queue"
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
