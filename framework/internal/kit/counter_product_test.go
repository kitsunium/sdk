package kit_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/authz"
)

// The product the tests of commands and queries run: an order counter, whose
// staff shows a badge — its auth data a kit.Principal —, and a lab of
// commands whose handlers a test holds, fails or counts. Declarations are
// package-level, as a product writes them; the positions test reads this
// file.

var Staff = kit.NewService("staff", "Badges the staff, for the tests.")

// StaffBadge is what a request proves itself with.
type StaffBadge struct {
	Token string `header:"X-Badge"`
}

// StaffMe is what a badge says about its holder: a kit.Principal, whose
// roles the counter's policy reads.
type StaffMe struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}

// Attrs gives the SDK's authz the holder's roles.
func (m StaffMe) Attrs() []authz.Attr { return []authz.Attr{authz.AttrStrings("roles", m.Roles...)} }

// staffBadges are the badges the counter knows.
var staffBadges = map[string]StaffMe{
	"alice": {Name: "Alice", Roles: []string{"customer"}},
	"bob":   {Name: "Bob", Roles: []string{"customer"}},
	"root":  {Name: "Root", Roles: []string{"admin"}},
	"guest": {Name: "Guest"},
}

var StaffAuth = Staff.AuthHandler("badge", StaffCheck)

// StaffCheck resolves a badge to its holder.
func StaffCheck(_ context.Context, b StaffBadge) (kit.UID, StaffMe, error) {
	me, ok := staffBadges[b.Token]
	if !ok {
		return "", StaffMe{}, kit.Unauthenticated("unknown badge")
	}
	return kit.UID(b.Token), me, nil
}

var Counter = kit.NewService("counter", "Takes orders, for the tests.")

// CounterOrder is one order.
type CounterOrder struct {
	ID       string   `json:"id"`
	Customer string   `json:"customer"`
	Lines    []string `json:"lines"`
	Canceled bool     `json:"canceled,omitempty"`
}

var CounterOrders = Counter.Store("orders", func(o CounterOrder) string { return o.ID })

// CounterEvents announces every order placed.
var CounterEvents = Counter.Topic[CounterOrder]("events")

// CounterSummary is what the counter knows of a customer's orders: derived
// from the events, never written by a command.
type CounterSummary struct {
	Customer string   `json:"customer"`
	Orders   []string `json:"orders"`
}

// CounterSummaries is the read side: a read model, kept by a projection.
var CounterSummaries = Counter.Store("summaries", func(s CounterSummary) string { return s.Customer }, kit.ReadModel())

var _ = Counter.Subscribe("summaries", CounterEvents, CounterProject)

// CounterProject keeps a customer's summary: idempotent, as a delivery may
// come twice.
func CounterProject(ctx context.Context, o CounterOrder) error {
	s, err := CounterSummaries.Get(ctx, o.Customer)
	if err != nil {
		s = CounterSummary{Customer: o.Customer}
	}
	if slices.Contains(s.Orders, o.ID) {
		return nil
	}
	s.Orders = append(s.Orders, o.ID)
	return CounterSummaries.Put(ctx, s)
}

// CounterPolicy lets a customer place an order, and an admin reindex them.
var CounterPolicy = authz.Must(authz.NewRBAC(authz.RBACConfig{RolesAttr: "roles", Grants: []authz.Grant{
	{Role: "customer", Permissions: []authz.Permission{{Action: "place", Resource: "order"}}},
	{Role: "admin", Permissions: []authz.Permission{{Action: "reindex", Resource: "order"}}},
}}))

// CounterCart is what an order is placed with.
type CounterCart struct {
	Lines []string `json:"lines" validate:"mincount=1"`
}

// CounterPlaced is what placing an order answers.
type CounterPlaced struct {
	ID string `json:"id"`
}

// CounterByID names an order.
type CounterByID struct {
	ID string `json:"id" path:"id" validate:"required"`
}

var CounterPlace = Counter.Command("place-order", CounterPlaceOrder).
	Allow(CounterPolicy, "place", "order").
	Expose("POST /orders")

var CounterCancel = Counter.Command("cancel-order", CounterCancelOrder, kit.Auth()).
	Authorize(CounterOwns).
	Key(func(in CounterByID) string { return in.ID }).
	Expose("POST /orders/{id}/cancel", kit.RateLimitPerClient(100, 100))

var CounterReindex = Counter.Command("reindex", CounterReindexAll, kit.Queued(), kit.MaxDeliveries(3)).
	Allow(CounterPolicy, "reindex", "order").
	Expose("POST /reindex")

var CounterMine = Counter.Query("my-orders", CounterMyOrders, kit.Auth()).Expose("GET /orders", kit.AnyUser())

var CounterGet = Counter.Query("order", CounterGetOrder).Authorize(CounterOwns)

var CounterTotals = Counter.Query("totals", CounterReadTotals, kit.Auth())

// CounterReadTotals reads the caller's summary, from the read model.
func CounterReadTotals(ctx context.Context, _ kit.EmptyValue) (CounterSummary, error) {
	me, _ := kit.UserID(ctx)
	return CounterSummaries.Get(ctx, string(me))
}

// CounterPlaceOrder places an order for the caller.
func CounterPlaceOrder(ctx context.Context, in CounterCart) (CounterPlaced, error) {
	me, _ := kit.UserID(ctx)
	o := CounterOrder{ID: kit.NewID("order"), Customer: string(me), Lines: in.Lines}
	if err := CounterOrders.Insert(ctx, o); err != nil {
		return CounterPlaced{}, err
	}
	return CounterPlaced{ID: o.ID}, CounterEvents.Publish(ctx, o)
}

// CounterCancelOrder cancels an order.
func CounterCancelOrder(ctx context.Context, in CounterByID) (kit.EmptyValue, error) {
	_, err := CounterOrders.Update(ctx, in.ID, func(o *CounterOrder) error {
		o.Canceled = true
		return nil
	})
	return kit.EmptyValue{}, err
}

// CounterOwns lets an order's customer through; anyone else learns nothing.
func CounterOwns(ctx context.Context, in CounterByID) error {
	o, err := CounterOrders.Get(ctx, in.ID)
	if me, _ := kit.UserID(ctx); err != nil || o.Customer != string(me) {
		return kit.NotFound("no such order")
	}
	return nil
}

// counterReindexed counts the reindexes, and says who asked each.
var counterReindexed struct {
	sync.Mutex
	by []kit.UID
}

// CounterReindexAll rebuilds the orders' index: it counts the orders.
func CounterReindexAll(ctx context.Context, _ kit.EmptyValue) (kit.EmptyValue, error) {
	uid, _ := kit.UserID(ctx)
	counterReindexed.Lock()
	counterReindexed.by = append(counterReindexed.by, uid)
	counterReindexed.Unlock()
	_, err := CounterOrders.Count(ctx)
	return kit.EmptyValue{}, err
}

// CounterMyOrders lists the caller's orders.
func CounterMyOrders(ctx context.Context, _ kit.EmptyValue) ([]CounterOrder, error) {
	me, _ := kit.UserID(ctx)
	all, err := CounterOrders.List(ctx)
	return slices.DeleteFunc(all, func(o CounterOrder) bool { return o.Customer != string(me) }), err
}

// CounterGetOrder reads one order.
func CounterGetOrder(ctx context.Context, in CounterByID) (CounterOrder, error) {
	return CounterOrders.Get(ctx, in.ID)
}

var Lab = kit.NewService("lab", "Commands a test holds, fails or counts.")

// LabInput names a run of the lab.
type LabInput struct {
	Key  string `json:"key"`
	Note string `json:"note,omitempty"`
}

// labGate holds the runs of a lab command until a test opens it, and counts
// those running.
type labGate struct {
	mu      sync.Mutex
	open    chan struct{}
	running atomic.Int32
	most    atomic.Int32
	ran     atomic.Int32
}

// hold makes the next runs wait until release.
func (g *labGate) hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = make(chan struct{})
	g.running.Store(0)
	g.most.Store(0)
	g.ran.Store(0)
}

// release lets every waiting run go on.
func (g *labGate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.open != nil {
		close(g.open)
		g.open = nil
	}
}

// pass is a run: counted, and held while the gate is closed.
func (g *labGate) pass(ctx context.Context) error {
	n := g.running.Add(1)
	defer g.running.Add(-1)
	for m := g.most.Load(); n > m && !g.most.CompareAndSwap(m, n); m = g.most.Load() {
	}
	g.mu.Lock()
	open := g.open
	g.mu.Unlock()
	defer g.ran.Add(1)
	if open == nil {
		return nil
	}
	select {
	case <-open:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	labKeyed  labGate
	labSlow   labGate
	labCrowd  labGate
	labQueued labGate
)

// labFailures fails the next runs of the flaky queued command.
var labFailures atomic.Int32

// labHandled records who each queued run acted for, and in which trace.
var labHandled struct {
	sync.Mutex
	users []kit.UID
}

var (
	LabKeyed = Lab.Command("keyed", func(ctx context.Context, in LabInput) (kit.EmptyValue, error) {
		return kit.EmptyValue{}, labKeyed.pass(ctx)
	}).Key(func(in LabInput) string { return in.Key })

	LabSlow = Lab.Command("slow", func(ctx context.Context, _ LabInput) (kit.EmptyValue, error) {
		return kit.EmptyValue{}, labSlow.pass(ctx)
	}, kit.Timeout(50*time.Millisecond)).Expose("POST /lab/slow", kit.Anyone())

	LabCrowded = Lab.Query("crowded", func(ctx context.Context, _ LabInput) (kit.EmptyValue, error) {
		return kit.EmptyValue{}, labCrowd.pass(ctx)
	}, kit.Bulkhead(1)).Expose("POST /lab/crowded", kit.Anyone())

	LabPanics = Lab.Command("panics", func(context.Context, LabInput) (int, error) {
		panic("canary: do-not-leak-command-7f3e")
	}).Expose("POST /lab/panics", kit.Anyone())

	// LabHidden refuses everyone with a text of its own, which the caller
	// never reads; LabBroken cannot tell, which is a refusal too.
	LabHidden = Lab.Query("hidden", func(context.Context, LabInput) (int, error) { return 1, nil }).
			Authorize(func(context.Context, LabInput) error { return kit.Forbidden("canary: you are not an admin") })
	LabBroken = Lab.Query("broken", func(context.Context, LabInput) (int, error) { return 1, nil }).
			Authorize(func(context.Context, LabInput) error { return errors.New("canary: the members store is down") })

	LabQueued = Lab.Command("queued", LabHandleQueued, kit.Queued(), kit.MaxDeliveries(3), kit.Parallelism(2)).
			Key(func(in LabInput) string { return in.Key })
)

// LabHandleQueued is the queued command's handler: held by its gate, failed
// while labFailures says so, and recording who it acted for.
func LabHandleQueued(ctx context.Context, in LabInput) (kit.EmptyValue, error) {
	if err := labQueued.pass(ctx); err != nil {
		return kit.EmptyValue{}, err
	}
	if labFailures.Add(-1) >= 0 {
		return kit.EmptyValue{}, errors.New("canary: flaky, do-not-leak-queued-2a9b")
	}
	uid, _ := kit.UserID(ctx)
	labHandled.Lock()
	labHandled.users = append(labHandled.users, uid)
	labHandled.Unlock()
	return kit.EmptyValue{}, nil
}
