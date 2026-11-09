package signal

import (
	"context"
	"syscall"
	"testing"
	"time"

	coreproc "github.com/kitsunium/sdk/internal/core/proc"
	"github.com/kitsunium/sdk/internal/kernel/clock"
)

// escalationRun drives escalate over a channel on a ManualClock and records
// what the policy ran.
type escalationRun struct {
	src      chan coreproc.Signal
	clk      *clock.ManualClock
	firsts   chan coreproc.Signal
	repeats  chan coreproc.Signal
	finished chan struct{}
}

func startEscalation(t *testing.T, window time.Duration) *escalationRun {
	t.Helper()
	r := &escalationRun{
		src:      make(chan coreproc.Signal),
		clk:      clock.NewManualClock(time.Unix(0, 0)),
		firsts:   make(chan coreproc.Signal, 4),
		repeats:  make(chan coreproc.Signal, 4),
		finished: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(func() { cancel(); <-r.finished })
	policy := EscalationValue{
		Window:   window,
		OnFirst:  func(s coreproc.Signal) { r.firsts <- s },
		OnRepeat: func(s coreproc.Signal) { r.repeats <- s },
	}
	go func() { defer close(r.finished); escalate(ctx, r.clk, policy, r.src) }()
	return r
}

// TestEscalateOnAManualClock pins #1096's policy without a real signal: two
// deliveries 100 ms apart inside a 1 s window run OnFirst then OnRepeat and
// end the relay; two 2 s apart run OnFirst twice and OnRepeat never; a zero
// window makes any later delivery the repeat.
func TestEscalateOnAManualClock(t *testing.T) {
	t.Parallel()
	term := coreproc.Signal(syscall.SIGTERM)
	t.Run("a repeat inside the window escalates once", func(t *testing.T) {
		t.Parallel()
		r := startEscalation(t, time.Second)
		r.src <- term
		<-r.firsts
		r.clk.BlockUntil(1)
		r.clk.Advance(100 * time.Millisecond)
		r.src <- term
		//: the repeat, once, and the relay ends.
		if got := <-r.repeats; got != term {
			t.Fatalf("OnRepeat(%v), want %v", got, term)
		}
		<-r.finished
		if len(r.firsts) != 0 || len(r.repeats) != 0 {
			t.Fatalf("extra hooks ran: %d first(s), %d repeat(s)", len(r.firsts), len(r.repeats))
		}
	})
	t.Run("two deliveries farther apart than the window are two firsts", func(t *testing.T) {
		t.Parallel()
		r := startEscalation(t, time.Second)
		r.src <- term
		<-r.firsts
		r.clk.BlockUntil(1)
		r.clk.Advance(2 * time.Second)
		r.src <- term
		<-r.firsts
		select {
		case s := <-r.repeats:
			t.Fatalf("OnRepeat(%v) ran for deliveries 2 s apart", s)
		case <-time.After(20 * time.Millisecond):
		}
	})
	t.Run("a zero window escalates any later delivery", func(t *testing.T) {
		t.Parallel()
		r := startEscalation(t, 0)
		r.src <- term
		<-r.firsts
		r.clk.Advance(time.Hour)
		r.src <- term
		if got := <-r.repeats; got != term {
			t.Fatalf("OnRepeat(%v), want %v", got, term)
		}
		<-r.finished
	})
}
