//go:build integration

// Store and forward (ADR 0185) over the queue's SQL broker on each real
// engine: batches kept by one forwarder are found and counted by the next
// over the same table, a collector down then back is retried on the
// forwarder's clock, and a batch the collector refused lies in the table's
// dead letters.
package sql_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/app/forward"
	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/data/queue"
	"github.com/kitsunium/sdk/pkg/v1/observe/trace"
)

// forwardBackoff is the forwarder's first retry wait, distinct from every
// other timer the case arms.
const forwardBackoff time.Duration = 7 * time.Second

// armedClock is a ManualClock that reports each timer once it is armed.
type armedClock struct {
	*clock.ManualClock
	armed chan time.Duration
}

// NewTimer arms the timer, then reports its duration.
func (c *armedClock) NewTimer(d time.Duration) clock.Timer {
	timer := c.ManualClock.NewTimer(d)
	select {
	case c.armed <- d:
	default:
	}
	return timer
}

// await waits for a timer of d to be armed.
func (c *armedClock) await(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.After(time.Minute)
	for {
		select {
		case got := <-c.armed:
			if got == d {
				return
			}
		case <-deadline:
			t.Fatalf("no timer of %v was armed", d)
		}
	}
}

// scripted answers each batch by its content: "flaky" fails once, retryably;
// "refused" fails for good; anything else is accepted.
type scripted struct {
	mu    sync.Mutex
	seen  map[string]int
	calls chan string
}

// send is the forwarder's Sender.
func (s *scripted) send(_ context.Context, batch []byte) error {
	s.mu.Lock()
	s.seen[string(batch)]++
	n := s.seen[string(batch)]
	s.mu.Unlock()
	s.calls <- string(batch)
	switch {
	case string(batch) == "refused":
		return trace.OTLPExportRejected
	case string(batch) == "flaky" && n == 1:
		return trace.OTLPExportUnavailable
	default:
		return nil
	}
}

// TestForwardOnEveryEngine runs the restart, the retry and the dead letter
// over one SQL queue per engine.
func TestForwardOnEveryEngine(t *testing.T) {
	eachEngine(t, func(t *testing.T, e *engine) {
		clk := &armedClock{ManualClock: clock.NewManualClock(queueEpoch), armed: make(chan time.Duration, 1024)}
		policy := queue.Policy{VisibilityTimeout: 96 * time.Hour, MaxDeliveries: 1_000_000}
		fx := openQueue(t, e, queue.SQLConfig{Policy: policy, Clock: clk})
		sender := &scripted{seen: map[string]int{}, calls: make(chan string, 16)}
		config := forward.Config{
			Broker: fx.broker, Send: sender.send, Retryable: trace.OTLPRetryable, Clock: clk,
			PollInterval: time.Hour,
		}
		config.Backoff.BaseDelay = forwardBackoff

		first, err := forward.New(t.Context(), config)
		must(t, err)
		for _, batch := range []string{"flaky", "refused", "fine"} {
			must(t, first.Enqueue(t.Context(), []byte(batch)))
		}

		// A second process over the same table: it counts what the first kept.
		second, err := forward.New(t.Context(), config)
		must(t, err)
		if stats := second.Stats(); stats.Pending != 3 || stats.PendingBytes != int64(len("flaky")+len("refused")+len("fine")) {
			t.Fatalf("the next forwarder counts %+v, want the three batches the first kept", stats)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- second.Run(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("Run = %v", err)
			}
		}()

		// Batches enqueued at one instant of the clock come out in the
		// broker's identifier order; the one consumer waits on the flaky
		// batch's backoff once it has failed, so the clock is advanced then.
		sent := map[string]int{}
		next := func() {
			select {
			case got := <-sender.calls:
				sent[got]++
			case <-time.After(time.Minute):
				t.Fatalf("batches never sent: %v", sent)
			}
		}
		for sent["flaky"] == 0 {
			next()
		}
		clk.await(t, forwardBackoff)
		clk.Advance(forwardBackoff)
		for sent["flaky"]+sent["refused"]+sent["fine"] < 4 {
			next()
		}
		if sent["flaky"] != 2 || sent["refused"] != 1 || sent["fine"] != 1 {
			t.Fatalf("sent %v, want flaky twice and the others once", sent)
		}
		deadline := time.Now().Add(time.Minute)
		for second.Stats().Sent != 2 || second.Stats().DeadLettered != 1 {
			if time.Now().After(deadline) {
				t.Fatalf("stats = %+v, want two sent and one dead-lettered", second.Stats())
			}
			runtime.Gosched()
		}
		if stats := second.Stats(); stats.Pending != 0 || stats.Retries != 1 {
			t.Errorf("stats = %+v, want nothing pending after one retry", stats)
		}
		reader, ok := fx.broker.(queue.DeadLetterReader)
		if !ok {
			t.Fatal("the SQL broker reads no dead letters")
		}
		// The forwarder counts its verdict before the consumer has the broker
		// record it, so the dead letter is waited for.
		var dead []queue.DeadLetter
		for len(dead) == 0 && time.Now().Before(deadline) {
			dead, err = reader.DeadLetters(t.Context(), 10)
			must(t, err)
			runtime.Gosched()
		}
		if len(dead) != 1 || string(dead[0].Message.Payload) != "refused" || dead[0].Reason != "OTLP_EXPORT_REJECTED" {
			t.Errorf("dead letters = %+v, want the refused batch with the collector's verdict", dead)
		}
	})
}
