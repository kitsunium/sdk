// Package resilience — the bounded-concurrency policy.
package resilience

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	coreres "github.com/kitsunium/sdk/internal/core/resilience"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// Test_bulkhead_Run pins the reject-fast contract, which is the whole point of
// the policy.
//
// A bulkhead exists to keep one slow dependency from consuming every worker, so
// a call beyond the limit must be refused IMMEDIATELY rather than queued. A
// queue would just move the exhaustion somewhere the caller cannot see, and the
// caller's own timeout would fire instead of a typed rejection they could
// handle.
func Test_bulkhead_Run(t *testing.T) {
	t.Parallel()
	opErr := errors.New("the operation failed")

	type tc struct {
		name  string
		slots int
		//: how many callers occupy a slot before the probe call.
		occupied int
		//: what the probe's own operation returns when it is admitted.
		opErr        error
		wantRejected bool
	}
	tests := []tc{
		{name: "an idle bulkhead admits", slots: 1},
		{name: "a partly used bulkhead admits", slots: 4, occupied: 2},
		{name: "a full bulkhead rejects", slots: 1, occupied: 1, wantRejected: true},
		{name: "a wider bulkhead, full", slots: 4, occupied: 4, wantRejected: true},
		{name: "an admitted operation's error propagates", slots: 1, opErr: opErr},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := NewBulkhead(c.slots)

		//: hold c.occupied slots for the duration of the probe.
		release := make(chan struct{})
		held := make(chan struct{}, c.occupied)
		//: Goroutine lifecycle: one goroutine per occupied slot. Each blocks in
		//: its operation until release is closed, which the deferred close
		//: below guarantees on every exit path, so none can outlive the case.
		var wg sync.WaitGroup
		for range c.occupied {
			wg.Go(func() {
				//: a holder's own Run always succeeds; a rejection here would
				//: mean the bulkhead was already full, which the case set up
				//: not to be.
				if err := b.Run(t.Context(), func(context.Context) error {
					held <- struct{}{}
					<-release
					return nil
				}); err != nil {
					t.Errorf("a holder was rejected: %v", err)
				}
			})
		}
		defer func() {
			close(release)
			wg.Wait()
		}()
		for range c.occupied {
			select {
			case <-held:
			case <-time.After(5 * time.Second):
				t.Fatal("a holder never claimed its slot")
			}
		}

		ran := false
		err := b.Run(t.Context(), func(context.Context) error {
			ran = true
			return c.opErr
		})

		if c.wantRejected {
			//: the rejection is typed, so a caller can shed load rather than
			//: guess from a timeout.
			if !kerrs.HasCode(err, coreres.CodeBulkheadFull) {
				t.Fatalf("Run = %v, want BULKHEAD_FULL", err)
			}
			//: and the operation must not have run at all.
			if ran {
				t.Error("a rejected call still ran the operation")
			}
			return
		}
		if !ran {
			t.Fatal("an admitted call did not run the operation")
		}
		//: an admitted operation's own error passes through untouched — the
		//: policy has no opinion about it.
		if !errors.Is(err, c.opErr) {
			t.Errorf("Run = %v, want %v", err, c.opErr)
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}

// Test_bulkhead_RunReleases pins that a slot is returned however the operation
// ends. A leaked slot narrows the bulkhead permanently: after enough panics or
// errors the policy would reject everything, which looks exactly like a
// dependency that has gone away.
func Test_bulkhead_RunReleases(t *testing.T) {
	t.Parallel()
	type tc struct {
		name   string
		rounds int
		opErr  error
	}
	tests := []tc{
		{name: "successful calls", rounds: 50},
		{name: "failing calls", rounds: 50, opErr: errors.New("failed")},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		b := NewBulkhead(1)
		for i := range c.rounds {
			err := b.Run(t.Context(), func(context.Context) error { return c.opErr })
			//: never the rejection: the previous call released its slot.
			if kerrs.HasCode(err, coreres.CodeBulkheadFull) {
				t.Fatalf("call %d was rejected — the slot leaked", i)
			}
		}
	}
	for _, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
