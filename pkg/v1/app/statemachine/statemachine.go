//go:generate gomarkdoc --output README.md --repository.url https://github.com/kitsunium/sdk --repository.default-branch main --repository.path /pkg/v1/app/statemachine .

// Package statemachine is a state-machine engine over stored entities (ADR
// 0120): declare the states an entity goes through and the transitions between
// them, hand the engine your store, and it moves entities along — on the
// events you fire, and by itself on timers, deadlines and guards.
//
//	def := statemachine.Define(func(o *Order) *Status { return &o.Status }).
//	    Initial(Pending).
//	    On("pay", Pending, Paid).
//	    After("abandon", 24*time.Hour, Pending, Cancelled).    // a duration in the state
//	    At("expire", Paid, Expired, func(o Order) (time.Time, bool) { return o.ShipBy, !o.ShipBy.IsZero() }).
//	    When("ready", Paid, Shipping, func(o Order) bool { return o.Packed }).
//	    OnEnter(Paid, chargeCard).                               // before the store: an error cancels
//	    OnTransition(publish)                                    // after the store: an error is reported
//
//	m, err := statemachine.New(ctx, def, &statemachine.Config[Order, Status]{
//	    Store: orders, Journal: records,
//	    Report: func(ctx context.Context, err error) { log.Print(err) }, // what no return value carries
//	})
//	go func() {
//	    if err := m.Run(ctx); err != nil {                       // LoopRunning: another loop is going
//	        log.Print(err)
//	    }
//	}()                                                          // fires timers, deadlines and guards
//	o, err := m.Start(ctx, Order{ID: "o-1"})                     // Pending, inserted
//	o, err = m.Fire(ctx, "o-1", "pay")                           // Paid, replaced
//
// # Your store is the source of truth
//
// The state is a field of your entity and the entity lives in your [Store]:
// the engine reads it with Get, creates with Insert and moves with Replace — a
// Replace that finds the entity gone stores nothing, so a transition never
// brings back an entity deleted meanwhile. Beside the store the machine keeps
// one [Record] per entity — its state, since when, its latest transitions — in
// memory and in the [Journal] you give it, which is what keeps an After timer
// counting across a restart. Tell the machine about writes it did not make
// with Machine.Changed and Machine.Deleted: a write can move a deadline or
// make a guard hold, and a state changed behind its back enters its record as
// of the moment the machine learns of it.
//
// # Transitions
//
// Transitions of one entity run one at a time, transitions of different
// entities concurrently. A transition runs the OnEnter hooks of the state
// entered, stores the entity, records the step, releases the entity and then
// runs the OnTransition hooks. A hook that panics fails what it was part of —
// an OnEnter hook its transition, an OnTransition hook only itself — and never
// leaves an entity locked. An OnEnter hook may change the entity but not its
// state or key, and must not fire its own machine (Reentrant); an
// OnTransition hook may.
//
// # The loop never polls
//
// Machine.Run keeps an agenda: each entity in a state a timer or a guard
// leaves has one entry, the instant its earliest automatic transition falls
// due, on a heap. The loop fires what is due — per entity, the first declared
// transition due — and sleeps until the next one or until a write wakes it,
// never sooner than Config.MinGap after its last run, so a burst of writes is
// one run. Finding the next transition due costs O(log N) where a loop that
// re-reads the store on every wake pays O(N); BENCH.md in the engine measures
// both. An entity whose transition fails is retried after Config.Backoff,
// counted per entity. Give the machine a clock.ManualClock (pkg/v1/clock) and
// a test drives every timer.
//
// # What it does not do
//
// It is not a durable workflow runtime: there is no replay, no activity, no
// compensation, and nothing survives a crash but what your store and your
// journal hold. A write to an entity that does not go through the machine can
// race with a transition of it; the store's Replace is the arbiter.
package statemachine
