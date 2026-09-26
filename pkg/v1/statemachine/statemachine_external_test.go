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

// status returns the stored order's status.
func (o *orders) status(key string) Status {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.byID[key].Status
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

// start is the instant the manual clocks start at.
var start = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// orderMachine declares an order's life: paid by an event, shipped by a guard
// the payment makes hold, closed a day after shipping.
func orderMachine() *statemachine.Definition[Order, Status] {
	return statemachine.Define(func(o *Order) *Status { return &o.Status }).
		Initial("pending").
		On("pay", "pending", "paid").
		When("ship", "paid", "shipped", func(o Order) bool { return o.Paid }).
		After("close", 24*time.Hour, "shipped", "closed").
		OnEnter("paid", func(_ context.Context, o *Order) error { o.Paid = true; return nil })
}

// TestTheFacadeRunsAMachineEndToEnd drives an order, started afresh in each
// case, with nothing internal imported and on a manual clock: a guard refused
// to a caller, an event and the guard it makes hold, and a timer that fires
// when due and is recorded as a delay.
func TestTheFacadeRunsAMachineEndToEnd(t *testing.T) {
	t.Parallel()
	type tc struct {
		drive func(t *testing.T, m *statemachine.Machine[Order, Status], store *orders, clk *clock.ManualClock)
		name  string
	}
	cases := []tc{
		{name: "a guard is the loop's, never a caller's", drive: func(t *testing.T, m *statemachine.Machine[Order, Status], _ *orders, _ *clock.ManualClock) {
			if _, err := m.Fire(t.Context(), "o-1", "ship"); !errs.HasCode(err, statemachine.CodeTransitionRefused) {
				t.Errorf("firing a guard = %v; want TRANSITION_REFUSED", err)
			}
		}},
		{name: "an event, then the guard it makes hold", drive: func(t *testing.T, m *statemachine.Machine[Order, Status], store *orders, _ *clock.ManualClock) {
			if _, err := m.Fire(t.Context(), "o-1", "pay"); err != nil {
				t.Fatal(err)
			}
			next, err := m.Step(t.Context())
			if err != nil || store.status("o-1") != "shipped" || !next.Equal(start) {
				t.Fatalf("Step() = %v, %v; order %s", next, err, store.status("o-1"))
			}
		}},
		{name: "a timer fires when due, recorded as a delay", drive: func(t *testing.T, m *statemachine.Machine[Order, Status], _ *orders, clk *clock.ManualClock) {
			if _, err := m.Fire(t.Context(), "o-1", "pay"); err != nil {
				t.Fatal(err)
			}
			for _, want := range []time.Time{start, start.Add(24 * time.Hour)} {
				if next, err := m.Step(t.Context()); err != nil || !next.Equal(want) {
					t.Fatalf("Step() = %v, %v; want %v", next, err, want)
				}
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
		}},
	}
	runCase := func(t *testing.T, c tc) {
		t.Helper()
		clk := clock.NewManualClock(start)
		store := &orders{byID: make(map[string]Order)}
		m, err := statemachine.New(t.Context(), orderMachine(), &statemachine.Config[Order, Status]{Store: store, Clock: clk})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Start(t.Context(), Order{ID: "o-1"}); err != nil {
			t.Fatal(err)
		}
		c.drive(t, m, store, clk)
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			runCase(t, c)
		})
	}
}
