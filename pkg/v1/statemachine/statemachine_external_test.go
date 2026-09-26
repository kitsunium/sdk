package statemachine_test

import (
	"context"
	"iter"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/kitsunium/sdk/pkg/v1/clock"
	"github.com/kitsunium/sdk/pkg/v1/errs"
	"github.com/kitsunium/sdk/pkg/v1/statemachine"
)

// Status is an order's state.
type Status string

// Order is the entity the facade test drives.
type Order struct {
	ID     string
	Status Status
	Paid   bool
}

// orders is the smallest Store a program writes: a map behind a lock.
type orders struct {
	byID map[string]Order
	mu   sync.Mutex
}

func (o *orders) Key(order Order) string { return order.ID }

func (o *orders) Get(_ context.Context, key string) (Order, bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	order, ok := o.byID[key]
	return order, ok, nil
}

func (o *orders) Insert(_ context.Context, order Order) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.byID[order.ID]; ok {
		return false, nil
	}
	o.byID[order.ID] = order
	return true, nil
}

func (o *orders) Replace(_ context.Context, order Order) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.byID[order.ID]; !ok {
		return false, nil
	}
	o.byID[order.ID] = order
	return true, nil
}

func (o *orders) All(context.Context) iter.Seq2[Order, error] {
	return func(yield func(Order, error) bool) {
		o.mu.Lock()
		all := slices.Collect(maps.Values(o.byID))
		o.mu.Unlock()
		for _, order := range all {
			if !yield(order, nil) {
				return
			}
		}
	}
}

// TestTheFacadeRunsAMachineEndToEnd drives an order through an event, a guard
// and a timer with nothing internal imported, on a manual clock.
func TestTheFacadeRunsAMachineEndToEnd(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	clk := clock.NewManualClock(start)
	def := statemachine.Define(func(o *Order) *Status { return &o.Status }).
		Initial("pending").
		On("pay", "pending", "paid").
		When("ship", "paid", "shipped", func(o Order) bool { return o.Paid }).
		After("close", 24*time.Hour, "shipped", "closed").
		OnEnter("paid", func(_ context.Context, o *Order) error { o.Paid = true; return nil })
	store := &orders{byID: make(map[string]Order)}
	m, err := statemachine.New(t.Context(), def, &statemachine.Config[Order, Status]{Store: store, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(t.Context(), Order{ID: "o-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Fire(t.Context(), "o-1", "ship"); !errs.HasCode(err, statemachine.CodeTransitionRefused) {
		t.Errorf("firing a guard = %v; want TRANSITION_REFUSED", err)
	}
	if _, err := m.Fire(t.Context(), "o-1", "pay"); err != nil {
		t.Fatal(err)
	}
	next, err := m.Step(t.Context())
	if err != nil || store.byID["o-1"].Status != "shipped" || !next.Equal(start) {
		t.Fatalf("Step() = %v, %v; order %+v", next, err, store.byID["o-1"])
	}
	next, err = m.Step(t.Context())
	if err != nil || !next.Equal(start.Add(24*time.Hour)) {
		t.Fatalf("Step() = %v, %v; want the close a day out", next, err)
	}
	clk.Advance(24 * time.Hour)
	if _, err := m.Step(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec, ok := m.Record("o-1")
	if !ok || rec.State != "closed" || len(rec.History) != 4 || rec.History[3].Trigger != statemachine.TriggerDelay {
		t.Fatalf("Record() = %+v", rec)
	}
	if census := m.Census(); census["closed"] != 1 || len(census) != 4 {
		t.Errorf("Census() = %v", census)
	}
}
