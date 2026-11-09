package pool_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/kitsunium/sdk/pkg/v1/concur/pool"
)

// ExampleNew runs a burst of tasks on four workers, then drains the pool.
func ExampleNew() {
	ctx := context.Background()
	p := pool.NewPool(ctx, pool.Config{Workers: 4, Queue: 16})
	var done atomic.Int64
	for range 16 {
		if err := p.Submit(ctx, func(context.Context) error {
			done.Add(1)
			return nil
		}); err != nil {
			return
		}
	}
	if err := p.Drain(ctx); err != nil {
		return
	}
	fmt.Println(done.Load())
	// Output: 16
}

// TestFacadeSubmitDrainAndRefusals walks the published surface: a burst
// runs, a Submit after Drain is refused with PoolClosed, and a pool with no
// queue and its worker busy refuses with PoolFull.
func TestFacadeSubmitDrainAndRefusals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	p := pool.NewPool(ctx, pool.Config{Workers: 2, Queue: 8})
	var ran atomic.Int64
	for range 8 {
		if err := p.Submit(ctx, func(context.Context) error { ran.Add(1); return nil }); err != nil {
			t.Fatalf("Submit: %v", err)
		}
	}
	if err := p.Drain(ctx); err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if ran.Load() != 8 {
		t.Errorf("ran %d of 8", ran.Load())
	}
	if err := p.Submit(ctx, func(context.Context) error { return nil }); !errors.Is(err, pool.PoolClosed) {
		t.Errorf("Submit after Drain = %v, want PoolClosed", err)
	}
	busy := pool.NewPool(ctx, pool.Config{})
	release := make(chan struct{})
	for busy.Submit(ctx, func(context.Context) error { <-release; return nil }) != nil {
	}
	if err := busy.Submit(ctx, func(context.Context) error { return nil }); !errors.Is(err, pool.PoolFull) {
		t.Errorf("Submit with the one worker busy = %v, want PoolFull", err)
	}
	close(release)
	if err := busy.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
