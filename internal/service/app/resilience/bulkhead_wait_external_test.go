// Package resilience_test — the bulkhead that queues (#482): a bounded queue,
// a bounded wait on an injected clock, and the zero that is NewBulkhead(1).
package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/app/resilience"
	"github.com/kitsunium/sdk/internal/kernel/clock"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
	svcres "github.com/kitsunium/sdk/internal/service/app/resilience"
)

// holder occupies a bulkhead slot until released, and says when it holds it.
type holder struct {
	entered chan struct{}
	release chan struct{}
}

// newHolder returns a holder whose operation has not started.
func newHolder() *holder {
	return &holder{entered: make(chan struct{}), release: make(chan struct{})}
}

// op is the operation that holds the slot.
func (h *holder) op(context.Context) error {
	close(h.entered)
	<-h.release
	return nil
}

// occupy runs h through b on a goroutine and returns once h holds its slot;
// the returned channel yields Run's result.
//
// GOROUTINE LIFECYCLE: the goroutine ends when the test closes h.release, and
// the test reads the channel before returning.
func occupy(t *testing.T, b coreres.Runner, h *holder) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- b.Run(t.Context(), h.op) }()
	<-h.entered
	return done
}

// TestBulkheadWaitZeroIsTheRejectingBulkhead pins the zero configuration: one
// slot and no queue, so a second call is rejected at once without waiting.
func TestBulkheadWaitZeroIsTheRejectingBulkhead(t *testing.T) {
	t.Parallel()
	for _, cfg := range []svcres.BulkheadWaitConfig{
		{},
		{Queue: 4},
		{MaxWait: time.Second},
		{Queue: -1, MaxWait: -time.Second},
	} {
		b := svcres.NewBulkheadWait(cfg)
		h := newHolder()
		done := occupy(t, b, h)
		err := b.Run(t.Context(), func(context.Context) error { t.Fatal("ran past a full bulkhead"); return nil })
		if !kerrs.HasCode(err, coreres.CodeBulkheadFull) {
			t.Fatalf("%+v: second call = %v, want BULKHEAD_FULL at once", cfg, err)
		}
		close(h.release)
		if err := <-done; err != nil {
			t.Fatalf("%+v: holder = %v", cfg, err)
		}
	}
}

// TestBulkheadWaitHandsAFreedSlotToTheWaiter pins the queue: a call that
// finds every slot taken waits, and runs as soon as one frees — before its
// wait runs out, with no time passing on the clock.
func TestBulkheadWaitHandsAFreedSlotToTheWaiter(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewBulkheadWait(svcres.BulkheadWaitConfig{Max: 1, Queue: 1, MaxWait: time.Second, Clock: manual})
	h := newHolder()
	held := occupy(t, b, h)
	waited := make(chan error, 1)
	ran := make(chan struct{})
	go func() {
		waited <- b.Run(t.Context(), func(context.Context) error { close(ran); return nil })
	}()
	//: the waiter armed its wait.
	manual.BlockUntil(1)
	select {
	case <-ran:
		t.Fatal("the waiter ran while the slot was held")
	default:
	}
	close(h.release)
	if err := <-held; err != nil {
		t.Fatalf("holder = %v", err)
	}
	if err := <-waited; err != nil {
		t.Fatalf("waiter = %v, want nil once the slot freed", err)
	}
	<-ran
	if manual.Pending() != 0 {
		t.Fatalf("%d timers left armed, want the wait's released", manual.Pending())
	}
}

// TestBulkheadWaitRejectsAtExactlyMaxWait pins the wait: a queued call is not
// rejected one nanosecond before MaxWait, and is rejected with BulkheadFull at
// it, never having run.
func TestBulkheadWaitRejectsAtExactlyMaxWait(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewBulkheadWait(svcres.BulkheadWaitConfig{Max: 1, Queue: 1, MaxWait: time.Second, Clock: manual})
	h := newHolder()
	held := occupy(t, b, h)
	waited := make(chan error, 1)
	go func() {
		waited <- b.Run(t.Context(), func(context.Context) error { return errors.New("ran past MaxWait") })
	}()
	manual.BlockUntil(1)
	manual.Advance(time.Second - time.Nanosecond)
	if manual.Pending() != 1 {
		t.Fatal("the wait ended before MaxWait")
	}
	manual.Advance(time.Nanosecond)
	if err := <-waited; !kerrs.HasCode(err, coreres.CodeBulkheadFull) {
		t.Fatalf("waiter = %v, want BULKHEAD_FULL at MaxWait", err)
	}
	close(h.release)
	if err := <-held; err != nil {
		t.Fatalf("holder = %v", err)
	}
}

// TestBulkheadWaitRejectsAtOnceWhenTheQueueIsFull pins the queue's bound: with
// every slot and every queue place taken, the next call is rejected without
// arming a wait.
func TestBulkheadWaitRejectsAtOnceWhenTheQueueIsFull(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewBulkheadWait(svcres.BulkheadWaitConfig{Max: 1, Queue: 1, MaxWait: time.Minute, Clock: manual})
	h := newHolder()
	held := occupy(t, b, h)
	waited := make(chan error, 1)
	go func() { waited <- b.Run(t.Context(), func(context.Context) error { return nil }) }()
	manual.BlockUntil(1)
	err := b.Run(t.Context(), func(context.Context) error { t.Fatal("ran past a full queue"); return nil })
	if !kerrs.HasCode(err, coreres.CodeBulkheadFull) {
		t.Fatalf("third call = %v, want BULKHEAD_FULL at once", err)
	}
	if manual.Pending() != 1 {
		t.Fatalf("%d waits armed, want only the queued call's", manual.Pending())
	}
	close(h.release)
	if err := <-held; err != nil {
		t.Fatalf("holder = %v", err)
	}
	if err := <-waited; err != nil {
		t.Fatalf("queued call = %v, want nil", err)
	}
}

// TestBulkheadWaitCancelledWaiterLeavesTheQueue pins the caller who leaves: a
// queued call whose context ends returns ctx.Err(), not BulkheadFull, and its
// queue place is free for the next call.
func TestBulkheadWaitCancelledWaiterLeavesTheQueue(t *testing.T) {
	t.Parallel()
	manual := clock.NewManualClock(time.Unix(1_700_000_000, 0))
	b := svcres.NewBulkheadWait(svcres.BulkheadWaitConfig{Max: 1, Queue: 1, MaxWait: time.Minute, Clock: manual})
	h := newHolder()
	held := occupy(t, b, h)
	ctx, cancel := context.WithCancel(t.Context())
	waited := make(chan error, 1)
	go func() { waited <- b.Run(ctx, func(context.Context) error { return errors.New("ran after leaving") }) }()
	manual.BlockUntil(1)
	cancel()
	if err := <-waited; !errors.Is(err, context.Canceled) || kerrs.HasCode(err, coreres.CodeBulkheadFull) {
		t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
	}
	//: the place it held is free: a new call queues rather than being rejected.
	again := make(chan error, 1)
	go func() { again <- b.Run(t.Context(), func(context.Context) error { return nil }) }()
	manual.BlockUntil(1)
	close(h.release)
	if err := <-held; err != nil {
		t.Fatalf("holder = %v", err)
	}
	if err := <-again; err != nil {
		t.Fatalf("next queued call = %v, want nil", err)
	}
}

// TestBulkheadWaitReleasesTheSlotOfAPanic pins that a panicking operation
// frees its slot, as the rejecting bulkhead's does.
func TestBulkheadWaitReleasesTheSlotOfAPanic(t *testing.T) {
	t.Parallel()
	b := svcres.NewBulkheadWait(svcres.BulkheadWaitConfig{Max: 1, Queue: 1, MaxWait: time.Second})
	func() {
		defer func() { _ = recover() }()
		_ = b.Run(t.Context(), func(context.Context) error { panic("canary") })
	}()
	if err := b.Run(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Fatalf("call after a panic = %v, want the slot back", err)
	}
}
